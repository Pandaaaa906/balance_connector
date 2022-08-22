package main

import (
	"github.com/gammazero/deque"
	"github.com/gin-gonic/gin"
	"go.bug.st/serial"
	"log"
	"net/http"
	"time"
)

const (
	CR = "\r"
	CL = "\n"
)

var (
	port        serial.Port
	dataQueue   deque.Deque[serialData]
	quit        chan bool
	respSuccess GenericResp
)

func main() {
	quit = make(chan bool)
	respSuccess = GenericResp{STATUS: "success", MSG: ""}

	router := gin.Default()
	router.GET("/comlist", getComList)
	router.POST("/open", openCom)
	router.GET("/read", read)
	router.GET("/close", close)
	err := router.Run("localhost:8080")
	if err != nil {
		quit <- true
	}
}

func getComList(c *gin.Context) {
	// TODO 获取端口列表
	var ports []string
	ports, err := serial.GetPortsList()
	if err != nil {
		log.Panicln(err)
	}
	var ret SerialPortList
	for _, portName := range ports {
		ret.PORTS = append(ret.PORTS, serialPort{NAME: portName})
	}
	c.SecureJSON(http.StatusOK, ret)
}

func openCom(c *gin.Context) {
	// TODO 打开接口

	var err error
	var args = serialOpenArgs{
		BaudRate: 1200,
		DataBits: 8,
		TimeOut:  5,
	}
	if err := c.BindJSON(&args); err != nil {
		log.Panicln(err)
		return
	}
	mode := &serial.Mode{
		BaudRate: args.BaudRate,
		DataBits: args.DataBits,
		Parity:   args.Parity,
		StopBits: args.StopBits,
	}
	port, err = serial.Open(args.PORT, mode)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusInternalServerError, GenericResp{"Fail", err.Error()})
		return
	}
	err = port.SetReadTimeout(time.Second * args.TimeOut)
	if err != nil {
		log.Println(err)
		c.JSON(http.StatusInternalServerError, GenericResp{"Fail", err.Error()})
		return
	}
	go goReadPort(port, args.EXPECTED)

	c.JSON(http.StatusOK, respSuccess)
}

func read(c *gin.Context) {
	// TODO 读取数据
	var data serialData
	if dataQueue.Len() > 0 {
		data = dataQueue.PopBack()
	} else {
		data = serialData{TIME: time.Now()}
	}
	c.JSON(http.StatusOK, data)
}

func close(c *gin.Context) {
	quit <- true
	err := port.Close()
	if err != nil {
		log.Panicln(err)
	}
	c.JSON(http.StatusOK, respSuccess)
}

func goReadPort(port serial.Port, expected string) {
	//
	if expected == "" {
		expected = CR
	}
	for {
		select {
		case <-quit:
			return
		default:
			data := serialReadUntil(port, expected)
			ret := serialData{
				time.Now(),
				data,
			}
			dataQueue.PushBack(ret)
			n := dataQueue.Len() - 1
			for i := 1; i <= n; i++ {
				dataQueue.PopFront()
			}
		}
	}
}
