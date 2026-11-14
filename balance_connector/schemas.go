package main

import (
	"time"
)

type serialPort struct {
	NAME string `json:"name"`
}

type SerialPortList struct {
	PORTS []serialPort `json:"ports"`
}

type serialOpenArgs struct {
	PORT     string        `json:"port"`
	BaudRate int           `json:"baudrate"`                                  // The serial port bitrate (aka Baudrate)
	DataBits int           `json:"databits"`                                  // Size of the character (must be 5, 6, 7 or 8)
	Parity   string        `json:"parity" binding:"validateParityChoice"`     // Parity (see Parity type for more info)
	StopBits string        `json:"stopbits" binding:"validateStopBitsChoice"` // Stop bits (see StopBits type for more info)
	EXPECTED string        `json:"expected"`
	TimeOut  time.Duration `json:"timeout"`
}

type serialData struct {
	TIME time.Time `json:"time"`
	DATA string    `json:"data"`
	STAT int       `json:"stat"`
}

type GenericResp struct {
	STATUS string `json:"status"`
	MSG    string `json:"msg"`
}
