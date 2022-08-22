package main

import (
	"go.bug.st/serial"
	"log"
)

func serialReadBytesUntil(port serial.Port, expected string) []byte {
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
		ret = append(ret, buff...)
		if buff[0] == []byte(expected)[0] {
			break
		}
	}
	return ret
}

func serialReadUntil(port serial.Port, expected string) string {
	buff := serialReadBytesUntil(port, expected)
	return string(buff)
}
