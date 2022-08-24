package main

import (
	"errors"
	"github.com/gammazero/deque"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"go.bug.st/serial"
	"net/http"
	"sort"
	"time"
)

const (
	CR = "\r"
	CL = "\n"
)

var (
	port             serial.Port
	dataQueue        deque.Deque[serialData]
	quit             chan bool
	respSuccess      = GenericResp{STATUS: "success", MSG: ""}
	portOpened       = false
	errPortOpened    = errors.New("port is opened")
	errPortNotOpened = errors.New("port is not opened")
)

func main() {
	quit = make(chan bool)
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterValidation("validateParityChoice", validateParityChoice)
		v.RegisterValidation("validateStopBitsChoice", validateStopBitsChoice)
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	router.Use(cors.New(config))

	router.GET("/comlist", getComList)
	router.POST("/open", openCom)
	router.GET("/read", read)
	router.GET("/close", close)

	err := router.Run("localhost:8333")
	if err != nil {
		quit <- true
	}
}

func getComList(c *gin.Context) {
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

func openCom(c *gin.Context) {
	// 打开接口
	var err error
	if portOpened {
		AbortMsg(http.StatusInternalServerError, errPortOpened, c)
	}
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
	mode := &serial.Mode{
		BaudRate: args.BaudRate,
		DataBits: args.DataBits,
		Parity:   ParityChoiceMap[args.Parity],
		StopBits: StopBitsChoiceMap[args.StopBits],
	}
	port, err = serial.Open(args.PORT, mode)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	err = port.SetReadTimeout(time.Second * args.TimeOut)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	portOpened = true
	go goReadPort(port, args.EXPECTED)
	c.JSON(http.StatusOK, respSuccess)
}

func read(c *gin.Context) {
	// 从数据缓冲池，读取数据，如果缓冲池为空，生成假数据
	// TODO 统一做成一个结构体去做，这里改简单点
	var data serialData
	if dataQueue.Len() > 0 {
		data = dataQueue.PopBack()
	} else {
		data = serialData{TIME: time.Now()}
	}
	c.JSON(http.StatusOK, data)
}

func close(c *gin.Context) {
	if !portOpened {
		AbortMsg(http.StatusInternalServerError, errPortNotOpened, c)
	}
	quit <- true
	portOpened = false
	err := port.Close()
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	c.JSON(http.StatusOK, respSuccess)
}

func goReadPort(port serial.Port, expected string) {
	// 不断读取端口数据，填入数据缓冲池，保持数据缓冲池大小等于1
	if expected == "" {
		expected = CR
	}
	for {
		select {
		case <-quit:
			return
		default:
			data := serialReadUntil(port, expected)
			var stat int
			if len(data) > 0 {
				stat = 1
			}
			ret := serialData{
				time.Now(),
				data,
				stat,
			}
			dataQueue.PushBack(ret)
			n := dataQueue.Len() - 1
			for i := 1; i <= n; i++ {
				dataQueue.PopFront()
			}
		}
	}
}
