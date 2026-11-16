# 多天平多会话实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 balance_connector 同时连接多台天平（每 COM 口一个 session），`/open` 返回 session 并以 Cookie/`?session=` 路由，`default_session_key` 兼容无凭证旧客户端；本分支自测后先发 `v0.0.4-rc1`，真机验证后再发正式 `v0.0.4`。

**Architecture:** 删掉全局单例（`port`/`portOpened`/`dataQueue`/`quit`），改为 `map[string]*serialSession` 注册表 + 插入序 slice（维护 default_session_key 顺延）；`goReadPort` 变为 session 方法；注册表加 `sync.Mutex`。通过包级变量 `openSerial = serial.Open` 接缝注入 fake 串口实现 TDD。

**Tech Stack:** Go 1.27、gin v1.12.0、go.bug.st/serial v1.8.0、gammazero/deque、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-16-multi-scale-sessions-design.md`

## Global Constraints

- 分支：`feature/multi-scale-sessions`（基线 `24c77f6` = 线上 0.0.3）；所有提交在本分支，**不合并 master 不发正式版**
- 本机 Go：`"/c/Program Files/Go/bin/go.exe"`（git-bash 无 go in PATH）
- session ID：`crypto/rand` 8 字节 → hex 16 字符
- Cookie：`bc_session`，host-only（Domain 为空），Path `/`，maxAge 0（浏览器会话期），HttpOnly
- session 解析优先级：`?session=` 参数 **>** cookie **>** default_session_key（插入序第一个存活 session，close 后从 order 删除即自然顺延）
- 错误文案（精确）：同 COM 重复 open → `port is opened`；凭证存在但无效 → `session not found`；无任何存活 session → `port is not opened`；全部 500 + `GenericResp`
- `/open` 成功响应：`{"status":"success","msg":"","session":"<id>"}`（`GenericResp.SESSION` 用 `omitempty`，失败响应 JSON 形状不变）
- 单 session 队列维持无锁假设（spec 明确不在本设计内修）；只有注册表（map+order）加锁
- `AbortMsg` 写响应后 panic（gin recovery 兜底），沿用现有不 return 的风格

## Review Focus

1. **Cookie 按 host 隔离**：`localhost` 与 `127.0.0.1` 的 cookie 不互通，客户端换 host 会"丢"cookie 而落到 default 路由——行为可接受（有兜底）但需知晓；httptest 无法覆盖，rc 真机验证时留意。
2. **并发 open 同一 COM 的 check-then-act 竞态**：两个请求同时 check 都通过时，第二个 `openSerial` 在 Windows 因设备占用失败 → 500，物理上不会双开同一口；顺序场景由 TestOpenSameComRejected 钉住，并发变体不做（flaky）。
3. **凭证存在但无效 vs 无凭证**的错误文案差异（`session not found` / `port is not opened`）是 spec 刻意的；TestReadSessionNotFound 钉住前者。
4. **fake 串口不覆盖真实超时/断线行为**（真实 Read 阻塞至 timeout 才返回 n=0；fake 是脚本读完即 5ms 睡眠）——由 rc 真机验证门兜底。
5. **`close` 阻塞时长 = 一个 read timeout**（真实 5s，fake 5ms）：`quit <- true` 要等 readLoop 回到 select；存量行为，本计划不改变。

---

### Task 1: 测试基础设施（fake 串口 + setupRouter 抽出）

**Files:**
- Modify: `balance_connector/main.go`（抽出 `setupRouter`，main 只留初始化和 Run）
- Modify: `balance_connector/main_test.go`（fakeSerialPort + resetRegistry + stubOpenSerial + waitForQueue；TestGetVersion 改用 setupRouter）

**Interfaces:**
- Produces: `type fakeSerialPort struct{ script []byte; pos int; mu sync.Mutex }`（实现 serial.Port 全部 12 个方法）；`func resetRegistry()`；`func stubOpenSerial(scripts map[string][]byte)`（替换包级 `openSerial`，t.Cleanup 恢复）；`func waitForQueue(t *testing.T, s *serialSession) serialData`；`func setupRouter() *gin.Engine`
- Consumes: 无（首个任务）

- [ ] **Step 1: 抽出 setupRouter（不改行为）**

`main()` 改为：

```go
func main() {
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterValidation("validateParityChoice", validateParityChoice)
		v.RegisterValidation("validateStopBitsChoice", validateStopBitsChoice)
	}
	gin.SetMode(gin.ReleaseMode)
	router := setupRouter()

	log.Printf("balance_connector %s listening on localhost:8333", version)
	if err := router.Run("localhost:8333"); err != nil {
		log.Panicln(err)
	}
}

func setupRouter() *gin.Engine {
	router := gin.Default()
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	router.Use(cors.New(config))

	router.GET("/comlist", apiGetComList)
	router.POST("/open", apiOpenCom)
	router.GET("/read", apiRead)
	router.GET("/close", apiClose)
	router.GET("/version", getVersion)
	return router
}
```

注意：`quit = make(chan bool)` 和 router.Run 错误分支的 `quit <- true` 一并删除（quit 是 Task 2 要删的旧全局）。

- [ ] **Step 2: 现有测试改用 setupRouter，确认仍绿**

把 `main_test.go` 中 `TestGetVersion` 的三行 `gin.SetMode(gin.TestMode)` / `gin.New()` / `router.GET(...)` 替换为：

```go
	gin.SetMode(gin.TestMode)
	router := setupRouter()
```

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -run TestGetVersion -v`
Expected: PASS（重构无行为变化）

- [ ] **Step 3: 添加测试辅助设施**

在 `main_test.go` 追加（imports 补 `sync`、`time`、`go.bug.st/serial`、`strings`——按需）：

```go
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
func (f *fakeSerialPort) Write(p []byte) (int, error)                  { return len(p), nil }
func (f *fakeSerialPort) Close() error                                 { return nil }
func (f *fakeSerialPort) SetMode(*serial.Mode) error                   { return nil }
func (f *fakeSerialPort) Drain() error                                 { return nil }
func (f *fakeSerialPort) ResetInputBuffer() error                      { return nil }
func (f *fakeSerialPort) ResetOutputBuffer() error                     { return nil }
func (f *fakeSerialPort) SetDTR(bool) error                            { return nil }
func (f *fakeSerialPort) SetRTS(bool) error                            { return nil }
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
```

注意：这些辅助引用了 Task 2 才创建的 `registryMu`/`sessions`/`order`/`serialSession`/`openSerial`——**本步只追加代码，编译会失败，属预期**；Task 2 完成后编译恢复。执行者勿在本步强行让它们编译（那是提前实现）。

- [ ] **Step 4: Commit**

```bash
git add balance_connector/main.go balance_connector/main_test.go
git commit -m "refactor: extract setupRouter, add fake serial port test fixtures"
```

---

### Task 2: 会话注册表 + POST /open 多 session 化

**Files:**
- Modify: `balance_connector/main.go`（删旧全局，加注册表/serialSession/openSerial/apiOpenCom 重写/readLoop 方法）
- Modify: `balance_connector/schemas.go`（GenericResp 加 SESSION 字段）
- Test: `balance_connector/main_test.go`（TestOpenCreatesSessionAndSetsCookie、TestOpenSameComRejected）

**Interfaces:**
- Consumes: Task 1 的测试辅助（此时编译恢复）
- Produces: `type serialSession struct { id, com string; port serial.Port; queue deque.Deque[serialData]; quit chan bool }`；`var openSerial = serial.Open`（签名为 `func(string, *serial.Mode) (serial.Port, error)`）；`var registryMu sync.Mutex; var sessions = map[string]*serialSession{}; var order []string`；`var errSessionNotFound = errors.New("session not found")`；`func newSessionID() string`；`func findSessionByComLocked(com string) *serialSession`；`func registerSession(s *serialSession)`；`func (s *serialSession) readLoop(expected string)`；`func defaultSessionLocked() *serialSession`

- [ ] **Step 1: 写失败测试**

`main_test.go` 追加：

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -run "TestOpen" -v`
Expected: 编译失败：`undefined: registryMu` / `undefined: openSerial` 等（Task 1 辅助设施引用的符号尚不存在）

- [ ] **Step 3: 实现注册表与 apiOpenCom 重写**

`main.go` 的 `var (...)` 块改为（删除 `port`/`dataQueue`/`quit`/`portOpened`，保留 `respSuccess`/`errPortOpened`/`errPortNotOpened`/`version`，新增注册表；imports 增加 `crypto/rand`、`encoding/hex`、`sync`）：

```go
var (
	respSuccess        = GenericResp{STATUS: "success", MSG: ""}
	errPortOpened      = errors.New("port is opened")
	errPortNotOpened   = errors.New("port is not opened")
	errSessionNotFound = errors.New("session not found")
	version            = "dev" // 构建时由 -ldflags "-X main.version=..." 覆盖

	openSerial = serial.Open // 测试可替换的打开端口接缝

	registryMu sync.Mutex
	sessions   = map[string]*serialSession{}
	order      []string // 插入序：order[0] 即 default_session_key
)

type serialSession struct {
	id    string
	com   string
	port  serial.Port
	queue deque.Deque[serialData]
	quit  chan bool
}

func newSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Panicln(err)
	}
	return hex.EncodeToString(b)
}

// 以下三个函数假定调用方已持有 registryMu
func findSessionByComLocked(com string) *serialSession {
	for _, s := range sessions {
		if s.com == com {
			return s
		}
	}
	return nil
}

func registerSession(s *serialSession) {
	sessions[s.id] = s
	order = append(order, s.id)
}

func defaultSessionLocked() *serialSession {
	if len(order) == 0 {
		return nil
	}
	return sessions[order[0]]
}
```

`apiOpenCom` 整体替换：

```go
func apiOpenCom(c *gin.Context) {
	var args = serialOpenArgs{
		BaudRate: 1200,
		DataBits: 8,
		TimeOut:  5,
		Parity:   "N",
		StopBits: "1",
	}
	if err := c.BindJSON(&args); err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	registryMu.Lock()
	dup := findSessionByComLocked(args.PORT)
	registryMu.Unlock()
	if dup != nil {
		AbortMsg(http.StatusInternalServerError, errPortOpened, c)
	}
	mode := &serial.Mode{
		BaudRate: args.BaudRate,
		DataBits: args.DataBits,
		Parity:   ParityChoiceMap[args.Parity],
		StopBits: StopBitsChoiceMap[args.StopBits],
	}
	p, err := openSerial(args.PORT, mode)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	if err = p.SetReadTimeout(time.Second * args.TimeOut); err != nil {
		p.Close()
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	s := &serialSession{
		id:   newSessionID(),
		com:  args.PORT,
		port: p,
		quit: make(chan bool),
	}
	registryMu.Lock()
	registerSession(s)
	registryMu.Unlock()
	go s.readLoop(args.EXPECTED)
	c.SetCookie("bc_session", s.id, 0, "/", "", false, true)
	c.JSON(http.StatusOK, GenericResp{STATUS: "success", SESSION: s.id})
}
```

`goReadPort` 替换为方法（`dataQueue`→`s.queue`、`quit`→`s.quit`、`port`→`s.port`，逻辑不变）：

```go
func (s *serialSession) readLoop(expected string) {
	// 不断读取端口数据，填入数据缓冲池，保持数据缓冲池大小等于1
	if expected == "" {
		expected = CR
	}
	for {
		select {
		case <-s.quit:
			return
		default:
			data := serialReadUntil(s.port, expected)
			var stat int
			if len(data) > 1 {
				stat = 1
			} else {
				continue
			}
			ret := serialData{
				time.Now(),
				data,
				stat,
			}
			s.queue.PushBack(ret)
			n := s.queue.Len() - 1
			for i := 1; i <= n; i++ {
				s.queue.PopFront()
			}
		}
	}
}
```

`apiRead` 本任务临时改为走 default（Task 3 换成完整解析）：

```go
func apiRead(c *gin.Context) {
	registryMu.Lock()
	s := defaultSessionLocked()
	registryMu.Unlock()
	if s == nil {
		AbortMsg(http.StatusInternalServerError, errPortNotOpened, c)
	}
	var data serialData
	if s.queue.Len() > 0 {
		data = s.queue.PopBack()
	} else {
		data = serialData{TIME: time.Now()}
	}
	c.JSON(http.StatusOK, data)
}
```

`schemas.go` 的 `GenericResp` 改为：

```go
type GenericResp struct {
	STATUS  string `json:"status"`
	MSG     string `json:"msg"`
	SESSION string `json:"session,omitempty"`
}
```

- [ ] **Step 4: 运行测试确认通过 + 全量套件**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -v`
Expected: 全部 PASS（含 TestOpenCreatesSessionAndSetsCookie、TestOpenSameComRejected、原 TestGetVersion）

- [ ] **Step 5: Commit**

```bash
git add balance_connector/main.go balance_connector/schemas.go balance_connector/main_test.go
git commit -m "feat: session registry, multi-scale open with cookie and same-COM guard"
```

---

### Task 3: session 解析（参数 > cookie > default）+ /read 路由

**Files:**
- Modify: `balance_connector/main.go`（加 `resolveSession`，`apiRead` 换用它）

**Interfaces:**
- Consumes: Task 2 的注册表与错误变量
- Produces: `func resolveSession(c *gin.Context) (*serialSession, error)`（错误为 `errSessionNotFound` / `errPortNotOpened`）；Task 4 的 apiClose 也消费它

- [ ] **Step 1: 写失败测试**

```go
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
	id7 := openSessionForTest(t, router, "COM7")
	_ = id7
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
```

- [ ] **Step 2: 运行确认失败**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -run "TestRead" -v`
Expected: FAIL（`resolveSession` undefined，或路由行为不符——TestReadByQueryParam 等落到 default 得到错误数据）

- [ ] **Step 3: 实现 resolveSession 并接入 apiRead**

```go
func resolveSession(c *gin.Context) (*serialSession, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if id := c.Query("session"); id != "" {
		if s, ok := sessions[id]; ok {
			return s, nil
		}
		return nil, errSessionNotFound
	}
	if id, err := c.Cookie("bc_session"); err == nil && id != "" {
		if s, ok := sessions[id]; ok {
			return s, nil
		}
		return nil, errSessionNotFound
	}
	if s := defaultSessionLocked(); s != nil {
		return s, nil
	}
	return nil, errPortNotOpened
}
```

`apiRead` 改为（替换 Task 2 的临时版本）：

```go
func apiRead(c *gin.Context) {
	s, err := resolveSession(c)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	var data serialData
	if s.queue.Len() > 0 {
		data = s.queue.PopBack()
	} else {
		data = serialData{TIME: time.Now()}
	}
	c.JSON(http.StatusOK, data)
}
```

- [ ] **Step 4: 运行测试确认通过 + 全量套件**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add balance_connector/main.go balance_connector/main_test.go
git commit -m "feat: resolve session by query param, cookie, or default_session_key"
```

---

### Task 4: /close + default 顺延

**Files:**
- Modify: `balance_connector/main.go`（`apiClose` 重写 + `unregisterSession`）

**Interfaces:**
- Consumes: `resolveSession`（Task 3）、`serialSession.quit/port`（Task 2）
- Produces: `func unregisterSession(id string)`

- [ ] **Step 1: 写失败测试**

```go
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
	if len(order) != 1 {
		t.Fatalf("expected 1 remaining session, got %d", len(order))
	}
	waitForQueue(t, sessions[order[0]])
	registryMu.Unlock()

	req = httptest.NewRequest(http.MethodGet, "/read", nil) // 无凭证 → 顺延到 COM7
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "2.000g") {
		t.Fatalf("expected fall-through to COM7 data, got %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -run "TestClose|TestDefault" -v`
Expected: FAIL（close 后 session 仍在——旧的 apiClose 操作的是旧全局）

- [ ] **Step 3: 实现**

```go
// unregisterSession 假定调用方已持有 registryMu
func unregisterSession(id string) {
	delete(sessions, id)
	for i, v := range order {
		if v == id {
			order = append(order[:i], order[i+1:]...)
			break
		}
	}
}
```

`apiClose` 整体替换：

```go
func apiClose(c *gin.Context) {
	s, err := resolveSession(c)
	if err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	s.quit <- true
	if err := s.port.Close(); err != nil {
		AbortMsg(http.StatusInternalServerError, err, c)
	}
	registryMu.Lock()
	unregisterSession(s.id)
	registryMu.Unlock()
	c.JSON(http.StatusOK, respSuccess)
}
```

- [ ] **Step 4: 运行测试确认通过 + 全量套件 + vet**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -v && "/c/Program Files/Go/bin/go.exe" vet ./balance_connector/`
Expected: 全部 PASS，vet 无输出

- [ ] **Step 5: Commit**

```bash
git add balance_connector/main.go balance_connector/main_test.go
git commit -m "feat: close session by id, default_session_key falls through on close"
```

---

### Task 5: workflow prerelease 标记 + 文档更新

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `README.md`（API 契约加 session）
- Modify: `CLAUDE.md`（架构描述）

**Interfaces:**
- Consumes: 无代码依赖；文档描述 Task 2-4 的行为

- [ ] **Step 1: workflow 自动标记 prerelease**

`.github/workflows/release.yml` 的 `Publish GitHub Release` 步骤加一行：

```yaml
      - name: Publish GitHub Release
        if: startsWith(github.ref, 'refs/tags/')
        uses: softprops/action-gh-release@v2
        with:
          files: build/*.msi
          generate_release_notes: true
          prerelease: ${{ contains(steps.ver.outputs.version, '-') }}
```

- [ ] **Step 2: README 更新**

`/open` 的 Response 200 示例改为：

```yaml
{
    "status": "success",
    "msg": "",
    "session": "a1b2c3d4e5f60708"   # 0.0.4 起新增；同时通过 Set-Cookie 种下 bc_session
}
```

在 `GET /close` 一节后追加：

```markdown
### 多 session（0.0.4 起）

服务可同时连接多台天平，每台对应一个 session：

- `POST /open` 返回 `session` 字段并种下 cookie `bc_session`（浏览器会话期）
- `GET /read`、`GET /close` 的 session 路由优先级：`?session=<id>` 查询参数 > `bc_session` cookie > **default_session_key**（最早 open 且仍存活的 session；被 close 后自动顺延到下一个）
- 旧客户端不带 cookie/参数时自动走 default_session_key，行为与单秤版一致
- 同一 COM 口重复 open 返回 500 `port is opened`
- 多 tab 同时读多台秤时，因 cookie 按站点共享，请各自拼 `?session=` 参数
```

`/read`、`/close` 的小标题下各加一行说明：`按 session 路由，规则见"多 session"一节`。

- [ ] **Step 3: CLAUDE.md 更新**

- 「架构」首段及 `main.go` 条目改为：注册表模型——`registryMu` 保护的 `sessions map[string]*serialSession` + `order []string`（`order[0]` 即 default_session_key）；每 session 独立 `queue`/`quit`/读 goroutine（`(*serialSession).readLoop`）；session 解析 `resolveSession`：参数 > cookie > default
- 「需要知道的坑」追加：**注册表有锁、单 session 队列无锁**（读 goroutine 写、HTTP 弹出，维持单客户端假设）；**cookie 按 host 隔离**（localhost ≠ 127.0.0.1，丢 cookie 时落 default 路由）
- 「接口契约」行补一句 session 机制指向 README

- [ ] **Step 4: YAML 语法检查 + 全量测试**

Run: `node -e "require('C:/Users/andrew.yip/AppData/Local/Temp/claude/yamlcheck/node_modules/yaml')" 2>/dev/null || (mkdir -p /tmp/yamlcheck2 && cd /tmp/yamlcheck2 && npm init -y >/dev/null 2>&1 && npm i yaml --silent >/dev/null 2>&1); node -e "const y=require('/tmp/yamlcheck2/node_modules/yaml');y.parse(require('fs').readFileSync('.github/workflows/release.yml','utf8'));console.log('YAML OK')"` && `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/`
Expected: YAML OK + 测试全绿（**在仓库根目录运行**）

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release.yml README.md CLAUDE.md
git commit -m "ci: auto-mark prerelease for -rc/-dev tags; docs: multi-session API contract"
```

---

### Task 6: 发布门（rc1）

**Files:** 无代码改动；发布操作

**Interfaces:**
- Consumes: 分支上全部任务完成、套件全绿

- [ ] **Step 1: 确认发布前置**

- 向用户确认：可以推分支 + rc tag（发布动作，需明确同意）
- `git log master..HEAD --oneline` 应只含本分支的功能/文档提交（master 无新提交混入）

- [ ] **Step 2: 推分支与 tag**

```bash
git push -u origin feature/multi-scale-sessions   # 网络抖动则重试
git tag v0.0.4-rc1
git push origin v0.0.4-rc1
```

- [ ] **Step 3: 监控 CI 至成功，验证 Release**

- 轮询 `https://api.github.com/repos/Pandaaaa906/balance_connector/actions/runs?per_page=1` 至 conclusion=success（网络问题重试推送时 tag 需先删后推）
- 验证 `releases/tags/v0.0.4-rc1` 存在、含 `balance_connector-0.0.4-rc1.msi`、**prerelease 标记为 true**

- [ ] **Step 4: 交接真机验证清单给用户**

1. 两台秤接两个 COM 口 → 各 open → `?session=` 读到各自重量
2. 旧客户端脚本不带 session → 走 default
3. 装了 0.0.3 的机器直接装 rc1 MSI（验证 MajorUpgrade：自动停旧服务、无重启提示）
4. 通过后：合并 master（用户确认后执行 `git checkout master && git merge feature/multi-scale-sessions && git push`），推 `v0.0.4` 正式 tag；**装了 rc1 的测试机先卸载再装正式版**（同 MSI 版本不被识别为升级）

---

## Self-Review 记录

- Spec 覆盖：session 创建+返回（Task 2）、同 COM 拒绝（Task 2）、cookie+参数路由（Task 3）、default_session_key 与顺延（Task 2/3/4）、错误消息表（Task 2/3 测试逐条断言）、close 删除（Task 4）、并发锁（Task 2 实现，Review Focus #2 说明竞态边界）、openSerial 接缝与 6 组用例（spec 验证策略 6 条 ↔ Task 2-4 共 9 个测试）、发布流程 rc→正式（Task 5/6）、README/CLAUDE.md（Task 5）。无遗漏。
- 占位符扫描：无 TBD；所有代码步骤含完整代码；每个测试有明确断言。
- 一致性：`resolveSession`/`registerSession`/`unregisterSession`/`defaultSessionLocked`/`findSessionByComLocked`/`openSerial`/`serialSession` 字段名跨任务一致；`GenericResp.SESSION` 在 Task 1 辅助与 Task 2 实现间一致；`order`/`sessions`/`registryMu` 命名全局一致。
- Review Focus 五项均有归属：#1 文档化为坑（Task 5）+ rc 验证；#2 TestOpenSameComRejected + 计划注释；#3 TestReadSessionNotFound / TestReadNoSessions；#4 rc 真机门（Task 6 Step 4）；#5 计划内注明存量行为。
