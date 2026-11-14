# Release Pipeline 设计：GitHub Actions 自动构建 + MSI 安装包

日期：2026-11-14
状态：待评审

## 背景与目标

balance_connector（Go 1.27，gin + go.bug.st/serial）目前发布靠手动：本机 `go build` + `go-msi` + 手写 `wix.json`（含写死的绝对路径 `D:\PythonScript\...`）。现要改为：

1. **版本管理**：当前 0.0.2 → 0.0.3，单一来源为 git tag，禁止版本号散落在文件中
2. **自动发布**：推 tag 即触发 GitHub Actions，自动完成 build → MSI 打包 → GitHub Release
3. **安装包**：MSI 内嵌 nssm，安装时注册为 Windows 服务（保持现有部署形态）

非目标：代码功能不变；不改动 HTTP API；不做代码签名（见"已知限制"）。

## 关键事实（调研结论）

- 项目托管在 **GitHub**（origin = GitHub）。GitHub Actions 免费：公开仓库不限分钟；私有仓库 2000 分钟/月（Windows runner 2 倍计费）。一次 release 构建约 5 分钟。
- `windows-latest` runner 自带 Go 与 .NET SDK，无需 Wine、无需本机安装任何工具。
- `go-msi`（mh-cbon/go-msi）已停更且依赖本机 WiX 3 → **弃用**，改用 WiX v4/v5 CLI（`dotnet tool install -g wix`，单命令 `wix build`）。
- exe 不是 Windows 服务感知程序 → 继续用 **nssm** 包装（正确选择，保留）。
- nssm 下载地址（用户提供）：`https://nssm.cc/release/nssm-2.24.zip`（内含 `win32/` 与 `win64/`）。

## 设计

### 1. 版本管理

- `main.go` 新增：
  ```go
  var version = "dev" // 构建时由 -ldflags "-X main.version=..." 覆盖
  ```
  并新增 `GET /version` 端点，返回 `{"version": "..."}`，用于现场核对部署版本；启动日志同时打印版本。
- 版本号单一来源：**git tag `v*`**。推 `v0.0.3` 触发发布。
- **规范化规则**（tag → MSI 版本）：去掉 `v` 前缀；只取 `major.minor.patch` 三段（丢弃 `-rc1` 等后缀，MSI `ProductVersion` 只接受 `x.y.z` 且每段 ≤ 65535）；`0.0.3-rc1` → MSI 版本 `0.0.3`，文件名用完整 tag 区分。

### 2. 发布流水线 `.github/workflows/release.yml`

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: windows-latest
```

步骤：
1. `actions/checkout` + `actions/setup-go`（go-version 读自 go.mod：1.27）
2. 解析版本：`$VERSION = tag 去 v 前缀`、`$MSI_VERSION = 三段规范化版本`
3. `go build -ldflags "-s -w -X main.version=$VERSION" -o build/ ./balance_connector`
4. 下载并解压 nssm-2.24.zip → 取 `win64/nssm.exe` 放入打包目录（PowerShell `Invoke-WebRequest` + `Expand-Archive`，加重试）
5. `dotnet tool install --global wix` → `wix build installer.wxs`
   参数传入版本：`wix build -d Version=$MSI_VERSION -d FileVersion=$VERSION installer.wxs`
6. 产物 `balance_connector-$VERSION.msi` 上传为 GitHub Release 资产（用 `softprops/action-gh-release`，支持同 tag 重复推送时更新，幂等）
7. （可选）`workflow_dispatch` 手动触发，便于不打 tag 调试流水线

### 3. 安装包 `installer.wxs`（替代 wix.json + go-msi）

- 全部**相对路径**：`build/balance_connector.exe`、nssm 解压产物。CI 与本地行为一致。
- `Product`：`Id="*"`（每次构建自动生成新 ProductCode），`UpgradeCode` 固定复用现有值 `db86ffd8-a4a2-4c4d-8140-c31ff42c9cf7`（保证 0.0.2 → 0.0.3 可识别为升级）；`Version=$(var.Version)`；含 `MajorUpgrade`（升级时自动卸载旧版）。
- 安装目录：`ProgramFiles64Folder\balance_connector`。
- **服务生命周期**（关键，现有 wix.json 的缺陷在此）：
  - 安装：`nssm install balance_connector "...balance_connector.exe"` + `nssm start`
  - 升级：**先停服务**（`ServiceControl` 元素，服务名 `balance_connector`，`Stop="both"`），否则文件被占用导致升级失败/要求重启。因为服务由 nssm 注册、WiX 不知情，必须用 `ServiceControl` 显式按服务名停止
  - 卸载：`nssm stop` + `nssm remove`（现有方案只 `sc delete`，服务在运行时删不干净；改用 nssm 自带命令保证先停后删）
  - 自定义动作需 `Execute="deferred"` + `Return="check"`，且排好执行顺序

### 4. 本地构建

- 根目录 `build.bat`：维持现状（仅编译 exe 到 `./build/`）。
- 新增 `distribution/build-installer.bat`（可选）：本地复现 CI 的 MSI 构建（需本机装 WiX：`dotnet tool install -g wix`）。保持 CI 为权威路径。

### 5. 删除

- `distribution/wix.json`、`distribution/build.bat` 中的 go-msi 部分
- go-msi 本机安装需求：不需要了（用户无需安装）

## 执行前置（松散的尾巴，必须先处理）

1. **gin 版本二选一落定**（当前工作区是 v1.10.0 无 mongo/quic 方案，备份在 /tmp 可能被清理；v1.12 方案的 go.mod/go.sum 也易重建）。落定时同步进 go.mod。
2. **所有未提交变更先 commit**（文件迁移 src/→balance_connector/、根 go.mod、build.bat、CLAUDE.md 等）。流水线 checkout 的是 tag 指向的 commit，工作区脏状态不会进包。

## 验证策略

1. 先推预发布 tag `v0.0.3-rc1` 跑通全流程（含 MSI 在一台 Windows 机器上安装→服务出现→`/version` 返回 0.0.3-rc1→卸载干净）
2. 无误后推正式 tag `v0.0.3`，发布 GitHub Release
3. 另测升级路径：机器上装 0.0.2 → 装 0.0.3 MSI，确认自动卸载旧版、服务替换、无需重启

## 文档更新

- `README.md`：新增"发布流程"章节（打 tag → Actions 自动出 MSI）
- `CLAUDE.md`：更新构建/发布命令、删除 go-msi 相关描述、新增 workflow 与 installer.wxs 说明

## 已知限制

- **MSI 无代码签名**：SmartScreen/杀毒软件可能提示。内部局域网工具可接受；如需消除，后续引入证书签名（另立任务）。
- nssm.cc 为个人站点，CI 下载若失败靠步骤重试兜底；若频繁失败可考虑把 nssm-2.24.zip 存入仓库或换镜像（暂不实现）。
