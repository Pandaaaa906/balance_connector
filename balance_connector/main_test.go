package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.bug.st/serial"
)

// fakeSerialPort 模拟一台秤：按脚本逐字节供数，读完后模拟真实串口超时（n=0）
type fakeSerialPort struct {
	script []byte
	pos    int
	mu     sync.Mutex
}

func (f *fakeSerialPort) Read(p []byte) (int, error) {
	f.mu.Lock()
	if f.pos < len(f.script) {
		n := copy(p, f.script[f.pos:])
		f.pos += n
		f.mu.Unlock()
		return n, nil
	}
	f.mu.Unlock()
	time.Sleep(5 * time.Millisecond) // 模拟超时后 n=0，避免空转
	return 0, nil
}
func (f *fakeSerialPort) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeSerialPort) Close() error                 { return nil }
func (f *fakeSerialPort) SetMode(*serial.Mode) error   { return nil }
func (f *fakeSerialPort) Drain() error                 { return nil }
func (f *fakeSerialPort) ResetInputBuffer() error      { return nil }
func (f *fakeSerialPort) ResetOutputBuffer() error     { return nil }
func (f *fakeSerialPort) SetDTR(bool) error            { return nil }
func (f *fakeSerialPort) SetRTS(bool) error            { return nil }
func (f *fakeSerialPort) GetModemStatusBits() (*serial.ModemStatusBits, error) {
	return &serial.ModemStatusBits{}, nil
}
func (f *fakeSerialPort) SetReadTimeout(time.Duration) error { return nil }
func (f *fakeSerialPort) Break(time.Duration) error          { return nil }

func TestGetVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := setupRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Version != version {
		t.Fatalf("expected version %q, got %q", version, body.Version)
	}
}
