package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func resetRegistry() {
	registryMu.Lock()
	sessions = map[string]*serialSession{}
	order = nil
	registryMu.Unlock()
}

// stubOpenSerial 替换包级 openSerial，按 COM 名返回各自独立的 fake 端口
func stubOpenSerial(t *testing.T, scripts map[string][]byte) {
	t.Helper()
	orig := openSerial
	openSerial = func(name string, mode *serial.Mode) (serial.Port, error) {
		return &fakeSerialPort{script: scripts[name]}, nil
	}
	t.Cleanup(func() { openSerial = orig })
}

func waitForQueue(t *testing.T, s *serialSession) serialData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.queue.Len() > 0 {
			return s.queue.PopBack()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("queue remained empty")
	return serialData{}
}

// openSessionForTest 通过 HTTP 打开一个 session，返回 session id
func openSessionForTest(t *testing.T, router *gin.Engine, com string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/open", strings.NewReader(`{"port":"`+com+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open %s: expected 200, got %d (%s)", com, rec.Code, rec.Body.String())
	}
	var resp GenericResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SESSION == "" {
		t.Fatal("open response missing session")
	}
	return resp.SESSION
}

func TestOpenCreatesSessionAndSetsCookie(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{"COM6": []byte("1.000g\r")})
	router := setupRouter()

	id := openSessionForTest(t, router, "COM6")
	if len(id) != 16 {
		t.Fatalf("expected 16-char session id, got %q", id)
	}
	// Set-Cookie: bc_session=<id>; Path=/; HttpOnly
	req := httptest.NewRequest(http.MethodPost, "/open", strings.NewReader(`{"port":"COM7"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "bc_session=") || !strings.Contains(setCookie, "HttpOnly") {
		t.Fatalf("Set-Cookie missing bc_session/HttpOnly: %q", setCookie)
	}
}

func TestOpenSameComRejected(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{"COM6": []byte("1.000g\r")})
	router := setupRouter()

	openSessionForTest(t, router, "COM6")
	req := httptest.NewRequest(http.MethodPost, "/open", strings.NewReader(`{"port":"COM6"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port is opened") {
		t.Fatalf("expected 'port is opened', got %s", rec.Body.String())
	}
}

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
