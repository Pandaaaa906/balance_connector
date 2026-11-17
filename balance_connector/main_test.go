package main

import (
	"encoding/json"
	"errors"
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
	script   []byte
	pos      int
	closeErr error // 非 nil 时 Close() 返回该错误
	mu       sync.Mutex
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
func (f *fakeSerialPort) Close() error                 { return f.closeErr }
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

// getSession 锁内按 id 查 session
func getSession(t *testing.T, id string) *serialSession {
	t.Helper()
	registryMu.Lock()
	defer registryMu.Unlock()
	return sessions[id]
}

// waitForQueue 等待队列出现读数并返回最新一条（只 peek 不弹出，/read 的行为仍被测试覆盖）。
// 自带 registryMu：即使 t.Fatal(Goexit) 也不会把锁带走。
func waitForQueue(t *testing.T, s *serialSession) serialData {
	t.Helper()
	registryMu.Lock()
	defer registryMu.Unlock()
	if s == nil {
		t.Fatal("session not found")
	}
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

	req := httptest.NewRequest(http.MethodPost, "/open", strings.NewReader(`{"port":"COM6"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp GenericResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.SESSION) != 16 {
		t.Fatalf("expected 16-char session id, got %q", resp.SESSION)
	}
	// Set-Cookie 的 bc_session 值必须与响应体 session 一致——这是 cookie 路由的机制本体
	// Set-Cookie: bc_session=<id>; Path=/; HttpOnly
	setCookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "bc_session="+resp.SESSION) || !strings.Contains(setCookie, "HttpOnly") {
		t.Fatalf("Set-Cookie missing matching bc_session/HttpOnly: %q", setCookie)
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
	waitForQueue(t, getSession(t, id6))
	waitForQueue(t, getSession(t, id7))

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
	waitForQueue(t, getSession(t, id6))

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
	first := order[0]
	registryMu.Unlock()
	waitForQueue(t, getSession(t, first))

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

func TestCloseRemovesSession(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{"COM6": []byte("1.000g\r")})
	router := setupRouter()
	id := openSessionForTest(t, router, "COM6")

	req := httptest.NewRequest(http.MethodGet, "/close?session="+id, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/read?session="+id, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected 500 session not found, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDefaultFallsThroughAfterClose(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	id6 := openSessionForTest(t, router, "COM6")
	openSessionForTest(t, router, "COM7")

	req := httptest.NewRequest(http.MethodGet, "/close?session="+id6, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req) // 关闭 default

	registryMu.Lock()
	n := len(order)
	first := ""
	if n > 0 {
		first = order[0]
	}
	registryMu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 remaining session, got %d", n)
	}
	waitForQueue(t, getSession(t, first))

	req = httptest.NewRequest(http.MethodGet, "/read", nil) // 无凭证 → 顺延到 COM7
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "2.000g") {
		t.Fatalf("expected fall-through to COM7 data, got %s", rec.Body.String())
	}
}

// 关闭非 default session（?session= 指到第二个 session）：default 不受影响
func TestCloseNonDefaultSessionByParam(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	openSessionForTest(t, router, "COM6")
	id7 := openSessionForTest(t, router, "COM7")
	waitForQueue(t, getSession(t, id7))

	req := httptest.NewRequest(http.MethodGet, "/close?session="+id7, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/read?session="+id7, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected 500 session not found, got %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/read", nil) // default 仍是 COM6
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "1.000g") {
		t.Fatalf("expected default COM6 unaffected, got %s", rec.Body.String())
	}
}

// 无凭证 close → 关 default session（旧客户端兼容路径），随后无凭证 read 顺延
func TestCloseDefaultsWithoutCredential(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	openSessionForTest(t, router, "COM6")
	id7 := openSessionForTest(t, router, "COM7")
	waitForQueue(t, getSession(t, id7)) // 等 COM7 出数，排除时序竞态

	req := httptest.NewRequest(http.MethodGet, "/close", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/read", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "2.000g") {
		t.Fatalf("expected fall-through to COM7, got %s", rec.Body.String())
	}
}

func TestCloseByCookie(t *testing.T) {
	resetRegistry()
	stubOpenSerial(t, map[string][]byte{
		"COM6": []byte("1.000g\r"),
		"COM7": []byte("2.000g\r"),
	})
	router := setupRouter()
	id6 := openSessionForTest(t, router, "COM6")
	openSessionForTest(t, router, "COM7")

	req := httptest.NewRequest(http.MethodGet, "/close", nil)
	req.AddCookie(&http.Cookie{Name: "bc_session", Value: id6})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/read?session="+id6, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected 500 session not found, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCloseNoSessions(t *testing.T) {
	resetRegistry()
	router := setupRouter()

	req := httptest.NewRequest(http.MethodGet, "/close", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "port is not opened") {
		t.Fatalf("expected 500 port is not opened, got %d %s", rec.Code, rec.Body.String())
	}
}

// 端口 Close 报错时 session 也必须已从注册表移除，不能留下"死 session"让 /read 永远返回空数据
func TestClosePortErrorStillRemovesSession(t *testing.T) {
	resetRegistry()
	orig := openSerial
	openSerial = func(name string, mode *serial.Mode) (serial.Port, error) {
		return &fakeSerialPort{script: []byte("1.000g\r"), closeErr: errors.New("close boom")}, nil
	}
	t.Cleanup(func() { openSerial = orig })
	router := setupRouter()
	id := openSessionForTest(t, router, "COM6")

	req := httptest.NewRequest(http.MethodGet, "/close?session="+id, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on close error, got %d (%s)", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/read?session="+id, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "session not found") {
		t.Fatalf("expected 500 session not found, got %d %s", rec.Code, rec.Body.String())
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
