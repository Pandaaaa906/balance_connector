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

// waitForQueue 等待队列出现读数并返回最新一条（只 peek 不弹出，/read 的行为仍被测试覆盖）
func waitForQueue(t *testing.T, s *serialSession) serialData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.queue.Len() > 0 {
			return s.queue.Back()
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

func TestReadByQueryParam(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	id6 := openSessionForTest(t, router, "COM6")
	id7 := openSessionForTest(t, router, "COM7")
	registryMu.Lock()
	waitForQueue(t, sessions[id6])
	waitForQueue(t, sessions[id7])
	registryMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/read?session="+id7, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "2.000g") {
		t.Fatalf("expected COM7 data, got %s", rec.Body.String())
	}
}

func TestReadByCookie(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	id6 := openSessionForTest(t, router, "COM6")
	openSessionForTest(t, router, "COM7")
	registryMu.Lock()
	waitForQueue(t, sessions[id6])
	registryMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.AddCookie(&http.Cookie{Name: "bc_session", Value: id6})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "1.000g") {
		t.Fatalf("expected COM6 data, got %s", rec.Body.String())
	}
}

func TestReadDefaultsToFirstOpened(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	openSessionForTest(t, router, "COM6")
	openSessionForTest(t, router, "COM7")
	registryMu.Lock()
	waitForQueue(t, sessions[order[0]])
	registryMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/read", nil) // 无 cookie 无参数
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "1.000g") {
		t.Fatalf("expected default(first) session data, got %s", rec.Body.String())
	}
}

func TestReadSessionNotFound(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{"COM6": []byte("1.000g\r")})
	router := setupRouter()
	openSessionForTest(t, router, "COM6")

	req := httptest.NewRequest(http.MethodGet, "/read?session=deadbeef", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected 'session not found', got %s", rec.Body.String())
	}
}

func TestReadNoSessions(t *testing.T) {
	resetRegistry()
	router := setupRouter()

	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port is not opened") {
		t.Fatalf("expected 'port is not opened', got %s", rec.Body.String())
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
