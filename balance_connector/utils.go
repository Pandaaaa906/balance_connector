package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"go.bug.st/serial"
)

const (
	CR = "\r"
	CL = "\n"
)

func serialReadBytesUntil(port serial.Port) []byte {
	ret := make([]byte, 0)
	for {
		buff := make([]byte, 1)
		n, err := port.Read(buff)
		if err != nil {
			log.Println(err)
			break
		}
		if n == 0 {
			break
		}
		if (buff[0] == []byte(CR)[0]) || (buff[0] == []byte(CL)[0]) {
			break
		}
		ret = append(ret, buff...)
	}
	return ret
}

func serialReadUntil(port serial.Port, expected string) string {
	buff := serialReadBytesUntil(port)
	buff = append(buff, []byte(expected)...)
	return string(buff)
}

func AbortMsg(code int, err error, c *gin.Context) {
	c.JSON(code, GenericResp{"failed", err.Error()})
	c.Abort()
	log.Panicln(err)
}

func removeDuplicateValues(intSlice []string) []string {
	keys := make(map[string]bool)
	var list []string

	// If the key(values of the slice) is not equal
	// to the already present value in new slice (list)
	// then we append it. else we jump on another element.
	for _, entry := range intSlice {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}
