package main

import (
	"github.com/go-playground/validator/v10"
	"go.bug.st/serial"
)

const (
	NoParity    string = "N"
	OddParity          = "O"
	EvenParity         = "E"
	MarkParity         = "M"
	SpaceParity        = "S"
)

var (
	ParityChoiceMap = map[string]serial.Parity{
		NoParity:    serial.NoParity,
		OddParity:   serial.OddParity,
		EvenParity:  serial.EvenParity,
		MarkParity:  serial.MarkParity,
		SpaceParity: serial.SpaceParity,
	}
)

var validateParityChoice validator.Func = func(fl validator.FieldLevel) bool {
	s, ok := fl.Field().Interface().(string)
	if !ok {
		return false
	}
	switch s {
	case NoParity, OddParity, EvenParity, MarkParity, SpaceParity:
		return true
	}
	return false
}

const (
	OneStopBit           string = "1"
	OnePointFiveStopBits string = "1.5"
	TwoStopBits          string = "2"
)

var (
	StopBitsChoiceMap = map[string]serial.StopBits{
		OneStopBit:           serial.OneStopBit,
		OnePointFiveStopBits: serial.OnePointFiveStopBits,
		TwoStopBits:          serial.TwoStopBits,
	}
)

var validateStopBitsChoice validator.Func = func(fl validator.FieldLevel) bool {
	s, ok := fl.Field().Interface().(string)
	if !ok {
		return false
	}
	switch s {
	case OneStopBit, OnePointFiveStopBits, TwoStopBits:
		return true
	}
	return false
}
