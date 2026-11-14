# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

Balance Connector（天平连接插件）：Windows 上的本地 HTTP 服务，把电子秤的串口（COM port）包装成 REST API，供网页/其他程序读取称重数据。

## 常用命令

```bash
./build.bat           # 一键构建，产物为 ./build/balance_connector.exe
go build -o ./build/ ./balance_connector   # 等价的手动命令（go build 不支持配置默认输出目录，必须用 -o 或脚本）
go run ./balance_connector                 # 本地运行（监听 localhost:8333）
```

- go.mod 位于仓库根目录（module 名为 `main`，Go 1.27），源码在 `./balance_connector/` 子目录。
- 无测试文件、无 lint 配置。
- 发布：见 `distribution/build.bat`，使用 `go-msi` 生成 MSI 安装包；`wix.json` 通过 `nssm` 把 exe 注册为 Windows 服务并在安装后启动。
- 注意：`wix.json` 中的文件路径硬编码为 `D:\PythonScript\balance_connector\build\balance_connector.exe`，本机仓库路径不同时需要同步修改。

## 架构

单一 `main` package（`balance_connector` 下 4 个文件），gin 提供 HTTP 接口，`go.bug.st/serial` 操作串口：

- `main.go` — HTTP 路由与核心业务逻辑。全局单例状态：`port`（打开的串口）、`portOpened`（标志位）、`dataQueue`（`deque.Deque[serialData]`）。
- 读数模型：`POST /open` 打开串口后启动后台 goroutine `goReadPort` 循环读串口，每读到一条完整数据（读到 `expected` 字符为止）就推入 `dataQueue`，并裁剪队列使其**只保留最新一条**读数。`GET /read` 从队列尾部弹出最新读数；队列为空时返回只有 `time` 的空数据。`quit` channel 用于关闭时终止读 goroutine。
- `schemas.go` — JSON 结构体与 validator 自定义 tag（`validateParityChoice` / `validateStopBitsChoice`），并在 `fields.go` 中提供字符串选项到 `serial.Parity` / `serial.StopBits` 的映射表。
- `utils.go` — `serialReadUntil`（逐字节读到分隔符）、`AbortMsg`、去重工具。
- 接口契约详见 `README.md`（`/open`、`/read`、`/close`、`/comlist`，响应统一为 `{status, msg}` 或 `{time, data, stat}`）。

## 需要知道的坑

- **错误处理用 panic**：`AbortMsg` 写入失败响应后调用 `log.Panicln(err)`，由 gin 的 recovery 中间件兜底。调用 `AbortMsg` 后**必须直接返回**，否则会继续执行后续代码（最近的提交专门修过这个 bug，见 `de695c5`）。
- **`timeout` 字段单位是秒**：JSON 直接绑定到 `time.Duration`，代码里再乘 `time.Second`，所以请求体里写的是秒数（如 `5`），不是 Go 的纳秒语义。
- 全局状态（`port`、`portOpened`、`dataQueue`）无锁保护；HTTP 处理与读 goroutine 并发访问，当前实现假定单客户端顺序调用。
- 服务硬编码监听 `localhost:8333` 且 CORS 允许所有来源——改动监听地址时注意 MSI/nssm 部署场景。
