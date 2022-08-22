package main

import (
	"go.bug.st/serial"
	"time"
)

type serialPort struct {
	NAME string `json:"name"`
	//DESC string `json:"desc"`
}

type SerialPortList struct {
	PORTS []serialPort `json:"ports"`
}

type serialOpenArgs struct {
	PORT     string          `json:"port"`
	BaudRate int             `json:"baudrate"` // The serial port bitrate (aka Baudrate)
	DataBits int             `json:"databits"` // Size of the character (must be 5, 6, 7 or 8)
	Parity   serial.Parity   `json:"parity"`   // Parity (see Parity type for more info)
	StopBits serial.StopBits `json:"stopbits"` // Stop bits (see StopBits type for more info)
	EXPECTED string          `json:"expected"`
	TimeOut  time.Duration   `json:"timeout"`
}

type serialData struct {
	TIME time.Time `json:"time"`
	DATA string    `json:"data"`
}

type GenericResp struct {
	STATUS string `json:"status"`
	MSG    string `json:"msg"`
}
