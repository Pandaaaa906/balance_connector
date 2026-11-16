# 多天平多会话 设计

日期：2026-11-16
状态：待评审
分支：`feature/multi-scale-sessions`（基线 `24c77f6`，即已发布的 0.0.3）

## 背景与目标

balance_connector 当前是单例模型：全局一个 `port`、一个 `dataQueue`，同时只能连一台秤。目标：一台服务同时连接多台天平（不同 COM 口），并保持对现有客户端的完全兼容。

**明确需求**（来自讨论）：
1. `POST /open` 创建 session 并返回 session 标识，后续 `/read`、`/close` 按 session 路由
2. session 传递用 **Cookie**（open 时种 `bc_session`，浏览器自动带回），同时支持 `?session=` 查询参数覆盖
3. **兼容旧客户端**：不带 cookie/参数的请求默认路由到 default_session_key（首次 open 的 session，关闭后顺延，见"默认 session 语义"）
4. **同一 COM 口不允许 open 两次**

非目标：不做 session 空闲超时；不改 `/comlist`、`/version`；不解决单 session 队列的无锁竞争（维持单客户端假设，见"并发模型"）。

**发布方式：本功能合并 master 后以 0.0.4 发布**（tag 驱动，CI 自动出 MSI；0.0.3→0.0.4 走既有 MajorUpgrade 升级路径，新版互升由本 wxs 的 `REMOVE="ALL"` 分支停服务，无重启问题）。

## API 契约

### session 解析优先级（`/read`、`/close` 共用）

`?session=<id>` 查询参数 **>** `bc_session` cookie **>** 默认（当前最早创建且仍存活的 session）。三者都未命中返回 500 + `GenericResp`。

### `POST /open`

- 请求体不变（port/baudrate/databits/parity/stopbits/expected/timeout）
- 同一 COM 口已有存活 session → 500 `{"status":"failed","msg":"port is opened"}`（沿用旧文案）
- 成功 → 建 session、起读 goroutine、种 cookie（浏览器会话期，无 Expires）、返回：
  ```json
  {"status":"success","msg":"","session":"a1b2c3d4"}
  ```
  （`GenericResp` 新增 `session` 字段，`omitempty`，失败响应 JSON 形状不变）

### `GET /read`

- 按优先级解析 session；找不到 → 500 `"session not found"`；无存活 session → 500 `"port is not opened"`（旧文案）
- 响应格式不变：`{"time":...,"data":...,"stat":...}`（取该 session 队列最新读数，空队列返回仅 time 的记录）

### `GET /close`

- 按优先级解析 session，关闭并从注册表删除
- 无 cookie/参数时关闭默认 session
- 无存活 session → 500 `"port is not opened"`

### 兼容矩阵

| 客户端类型 | /open | /read / /close |
|---|---|---|
| 旧客户端（无 cookie 无参数） | 建 session（多开不同 COM 均成功） | 路由到最早存活的 session |
| 新浏览器客户端 | 自动种/带 cookie | 各自 session |
| 新客户端多 tab 同时读多台秤 | 各 tab open 后记录 session | 用 `?session=` 区分（cookie 是同站共享的，多 tab 必须拼参数） |

### 默认 session 语义（default_session_key）

服务端把**首次 open 生成的 session id 记录为 default_session_key**，用于适配不带 cookie/参数的旧客户端：这类请求一律路由到 default session。

- default session 被 close 后，default_session_key **顺延**到下一个最早创建的存活 session（保证旧客户端只要还有任何存活 session 就能用）
- 全部 session 关闭后，旧客户端拿到 500 `"port is not opened"`（与旧版行为一致）

## 服务端设计

### 结构

```go
type serialSession struct {
    id    string          // crypto/rand 8 字节 → hex 16 位
    com   string
    port  serial.Port
    queue deque.Deque[serialData]
    quit  chan bool
}

var (
    registryMu sync.Mutex
    sessions   = map[string]*serialSession{}
    order      = []string{} // 插入序，维护"第一个"语义
)
```

- 现有全局 `port`/`portOpened`/`dataQueue`/`quit` 全部删除
- `goReadPort` 改为 `(*serialSession).readLoop(expected)`，每 session 独立 goroutine + 独立队列（仍裁剪为 1）
- 注册表（map + order）所有访问必须持 `registryMu`——与旧代码不同，多 session 下 `/open`、`/close`、`/read` 并发访问注册表是常态，这次必须加锁
- 单 session 内队列仍由读 goroutine 写入 + HTTP 处理弹出，维持"单客户端顺序调用"假设（与现状一致，不在本设计内修）

### 可测试性接缝

`serial.Open` 需要真实硬件，单测不可达。引入包级变量：

```go
var openSerial = serial.Open
```

生产代码经它打开端口；测试替换为 stub 返回 fake `serial.Port`（实现 `Read` 返回预置字节、`SetReadTimeout`/`Close` 空操作），从而 TDD 覆盖：

1. open 创建 session 且响应含 session 字段 + cookie 被种下
2. 同 COM 二次 open → 500 "port is opened"
3. 两个不同 COM 并存，按 `?session=` 读到各自数据
4. cookie 路由（带 `bc_session` cookie → 对应 session）
5. 无 cookie 无参数 → 默认第一个 session；关闭默认后顺延
6. close 后该 session 消失，再读 → "session not found" / "port is not opened"

## 错误消息表（新增/沿用）

| 场景 | HTTP | msg |
|---|---|---|
| 同 COM 重复 open | 500 | `port is opened`（沿用） |
| session 不存在 | 500 | `session not found`（新增） |
| 无存活 session | 500 | `port is not opened`（沿用） |

## 发布说明

- 合并 master 后推 tag `v0.0.4`，CI 自动构建 MSI 并创建 Release；真机验证（两台秤 + 旧客户端兼容）通过后对外部署
- README API 契约更新（`/open` 响应含 `session`、cookie/`?session=` 用法、default_session_key 语义）和 CLAUDE.md 架构描述（单例 → 注册表）**纳入本分支**，随 0.0.4 一起上线，保证线上文档与线上一致

## 验证策略

1. `go test ./balance_connector/` 全绿（新增上述 6 组用例）
2. 真机：两台秤接两个 COM 口，open 两次 → 各自 `?session=` 读到各自重量；旧客户端脚本不带 session 走默认路径
