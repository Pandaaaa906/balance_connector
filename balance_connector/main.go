package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"go.bug.st/serial"
)

var (
	respSuccess        = GenericResp{STATUS: "success", MSG: ""}
	errPortOpened      = errors.New("port is opened")
	errPortNotOpened   = errors.New("port is not opened")
	errSessionNotFound = errors.New("session not found")
	version            = "dev" // 构建时由 -ldflags "-X main.version=..." 覆盖

	openSerial = serial.Open // 测试可替换的打开端口接缝

	registryMu sync.Mutex
	sessions   = map[string]*serialSession{}
	order      []string // 插入序：order[0] 即 default_session_key
)

type serialSession struct {
	id    string
	com   string
	port  serial.Port
	queue deque.Deque[serialData]
	quit  chan bool // cap 1：close 握手不依赖 readLoop 即时接收
	closeOnce sync.Once
}

// shutdown 幂等关闭：quit 握手 + 关闭端口，并发/重复调用只会执行一次
func (s *serialSession) shutdown() error {
	var err error
	s.closeOnce.Do(func() {
		s.quit <- true
		err = s.port.Close()
	})
	return err
}

func newSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Panicln(err)
	}
	return hex.EncodeToString(b)
}

// 以下函数假定调用方已持有 registryMu
func findSessionByComLocked(com string) *serialSession {
	for _, s := range sessions {
		if s.com == com {
			return s
		}
	}
	return nil
}

func registerSession(s *serialSession) {
	sessions[s.id] = s
	order = append(order, s.id)
}

func defaultSessionLocked() *serialSession {
	if len(order) == 0 {
		return nil
	}
	return sessions[order[0]]
}

func init() {
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterValidation("validateParityChoice", validateParityChoice)
		v.RegisterValidation("validateStopBitsChoice", validateStopBitsChoice)
	}
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	router := setupRouter()

	log.Printf("balance_connector %s listening on localhost:8333", version)
	if err := router.Run("localhost:8333"); err != nil {
		log.Panicln(err)
	}
}

func setupRouter() *gin.Engine {
	router := gin.Default()

	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	router.Use(cors.New(config))

	router.GET("/comlist", apiGetComList)
	router.POST("/open", apiOpenCom)
	router.GET("/read", apiRead)
	router.GET("/close", apiClose)
	router.GET("/version", getVersion)
	return router
}

func apiGetComList(c *gin.Context) {
	// 获取端口列表
	var ports []string
	ports, err := serial.GetPortsList()
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	ports = removeDuplicateValues(ports)
	sort.Strings(ports)
	var ret SerialPortList
	for _, portName := range ports {
		ret.PORTS = append(ret.PORTS, serialPort{NAME: portName})
	}
	c.SecureJSON(http.StatusOK, ret)
}

func apiOpenCom(c *gin.Context) {
	var args = serialOpenArgs{
		BaudRate: 1200,
		DataBits: 8,
		TimeOut:  5,
		Parity:   "N",
		StopBits: "1",
	}
	if err := c.BindJSON(&args); err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	registryMu.Lock()
	dup := findSessionByComLocked(args.PORT)
	registryMu.Unlock()
	if dup != nil {
		AbortMsg(http.StatusInternalServerError, errPortOpened, c)
	}
	mode := &serial.Mode{
		BaudRate: args.BaudRate,
		DataBits: args.DataBits,
		Parity:   ParityChoiceMap[args.Parity],
		StopBits: StopBitsChoiceMap[args.StopBits],
	}
	p, err := openSerial(args.PORT, mode)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	if err = p.SetReadTimeout(time.Second * args.TimeOut); err != nil {
		p.Close()
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	s := &serialSession{
		id:   newSessionID(),
		com:  args.PORT,
		port: p,
		quit: make(chan bool, 1),
	}
	registryMu.Lock()
	registerSession(s)
	registryMu.Unlock()
	go s.readLoop(args.EXPECTED)
	c.SetCookie("bc_session", s.id, 0, "/", "", false, true)
	c.JSON(http.StatusOK, GenericResp{STATUS: "success", SESSION: s.id})
}

func resolveSession(c *gin.Context) (*serialSession, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if id := c.Query("session"); id != "" {
		if s, ok := sessions[id]; ok {
			return s, nil
		}
		return nil, errSessionNotFound
	}
	if id, err := c.Cookie("bc_session"); err == nil && id != "" {
		if s, ok := sessions[id]; ok {
			return s, nil
		}
		return nil, errSessionNotFound
	}
	if s := defaultSessionLocked(); s != nil {
		return s, nil
	}
	return nil, errPortNotOpened
}

func apiRead(c *gin.Context) {
	s, err := resolveSession(c)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	var data serialData
	if s.queue.Len() > 0 {
		data = s.queue.PopBack()
	} else {
		data = serialData{TIME: time.Now()}
	}
	c.JSON(http.StatusOK, data)
}

// unregisterSession 假定调用方已持有 registryMu
func unregisterSession(id string) {
	delete(sessions, id)
	for i, v := range order {
		if v == id {
			order = append(order[:i], order[i+1:]...)
			break
		}
	}
}

func apiClose(c *gin.Context) {
	s, err := resolveSession(c)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	// 先摘除注册表：close 重试/并发会拿到 session not found，不会卡在 quit 握手上；
	// 即使后续端口关闭报错，也不留"死 session"
	registryMu.Lock()
	unregisterSession(s.id)
	registryMu.Unlock()
	if err := s.shutdown(); err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	c.JSON(http.StatusOK, respSuccess)
}

func (s *serialSession) readLoop(expected string) {
	// 不断读取端口数据，填入数据缓冲池，保持数据缓冲池大小等于1
	if expected == "" {
		expected = CR
	}
	for {
		select {
		case <-s.quit:
			return
		default:
			data := serialReadUntil(s.port, expected)
			var stat int
			if len(data) > 1 {
				stat = 1
			} else {
				continue
			}
			ret := serialData{
				time.Now(),
				data,
				stat,
			}
			s.queue.PushBack(ret)
			n := s.queue.Len() - 1
			for i := 1; i <= n; i++ {
				s.queue.PopFront()
			}
		}
	}
}

func getVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": version})
}
