# Release Pipeline 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 GitHub Actions 实现 tag 驱动的自动发布：构建注入版本号的 exe → 打包内嵌 nssm 的 MSI → 发布 GitHub Release。

**Architecture:** git tag（`v*`）为版本唯一来源，推 tag 触发 `.github/workflows/release.yml`（windows-latest runner），WiX v4/v5 CLI 将 `installer.wxs` 编译为 MSI，替代已停更的 go-msi + `wix.json`（根除写死路径）。exe 通过 nssm 注册为 Windows 服务，生命周期（装/卸/升级先停服务）由 MSI 自定义动作管理。

**Tech Stack:** Go 1.27、gin v1.12.0（已定版）、WiX Toolset v4/v5（dotnet tool）、nssm 2.24、GitHub Actions（windows-latest）。

**Spec:** `docs/superpowers/specs/2026-11-14-release-pipeline-design.md`

## Global Constraints

- Go 版本：`go 1.27`（go.mod 位于仓库根目录，源码在 `./balance_connector/`）
- gin 版本：`v1.12.0`（已落定，含 mongo-driver/quic-go 间接依赖，接受）
- 触发方式：仅 `push: tags: ["v*"]`（另备 `workflow_dispatch` 调试）；版本号只来自 tag，禁止写死在代码里
- MSI `ProductVersion` 只接受 `x.y.z` 纯数字三段，每段 ≤ 65535，无 `v` 前缀
- `UpgradeCode` 固定为 `db86ffd8-a4a2-4c4d-8140-c31ff42c9cf7`（复用现有值，保证 0.0.2→0.0.3 升级识别）
- `installer.wxs` 中所有 `Source` 路径必须是相对仓库根目录的路径（`build/...`），禁止绝对路径
- nssm 下载地址：`https://nssm.cc/release/nssm-2.24.zip`，打包用其中 `win64/nssm.exe`
- 服务名：`balance_connector`；监听 `localhost:8333`（不变）
- 本机 Go 路径：`"/c/Program Files/Go/bin/go.exe"`（git-bash 环境 PATH 无 go）

## Review Focus

1. **带服务运行时升级**：旧版服务正在跑时安装新版 MSI，必须能自动停服务、替换文件、重启服务，不出现"文件正在使用"或要求重启。wxs 的 `ServiceControl` + 自定义动作顺序是本计划的正确性核心，但最终只能靠真机装 0.0.2→0.0.3 验证（见 Task 5 手动验证门）。
2. **deferred 自定义动作的 `[INSTALLDIR]` 展开**：`ExeCommand` 中的 `[INSTALLDIR]` 由 MSI 引擎在 deferred 执行时格式化——若 wxs 属性引用写法错误，安装时 nssm 找不到 exe 路径。同样由真机验证兜底。
3. **tag 规范化边界**：`v0.0.3` → MSI `0.0.3`；`v0.0.3-rc1` → MSI 仍 `0.0.3`（MSI 不认后缀）、文件名 `balance_connector-0.0.3-rc1.msi`。规范化逻辑在 workflow 的 pwsh 步骤里，无自动化测试，发布前用 rc tag 验证。
4. **ldflags 注入是否真的生效**：`go build -ldflags "-X main.version=..."` 只对 string **变量**生效；若写成常量或不匹配包路径，`/version` 永远返回 `dev` 且构建不报错。Task 1 用注入后的二进制实测 `/version` 来钉住。
5. **nssm.cc 下载失败**：个人站点，CI 网络抖动会直接断流水线。workflow 用 `-MaximumRetryCount 3` 缓解；若仍频繁失败再考虑 vendoring（spec 已知限制，暂不实现）。

---

### Task 0: 提交存量变更

**Files:**
- Modify: 全部已暂存/未暂存变更（文件迁移 src/→balance_connector/、根目录 go.mod+go.sum、`distribution/build.bat`、`build/balance_connector.exe` 被忽略不提交、`CLAUDE.md`、本 spec 与 plan 文档）

**Interfaces:**
- Produces: 干净的 git 基线——后续所有任务在此之上提交；tag 必须打在已提交状态上（流水线 checkout 的是 tag 指向的 commit）

- [ ] **Step 1: 确认 build/ 已被 .gitignore 忽略（不提交 exe）**

Run: `cd "C:\Users\andrew.yip\Projects\balance_connector" && git status --short --ignored | grep build`
Expected: `!! build/`（被忽略）

- [ ] **Step 2: 提交全部变更**

```bash
cd "C:\Users\andrew.yip\Projects\balance_connector"
git add -A
git commit -m "migrate to root go.mod (go 1.27), upgrade gin to v1.12.0, add build.bat and release pipeline spec"
```

Expected: 提交成功，工作区干净（`git status` 无未跟踪/修改）

---

### Task 1: 版本变量 + `/version` 端点（TDD）

**Files:**
- Modify: `balance_connector/main.go`
- Test: `balance_connector/main_test.go`（新建，项目首个测试文件）

**Interfaces:**
- Produces: `var version = "dev"`（main 包级 string 变量，构建时 `-X main.version=...` 覆盖）；`func getVersion(c *gin.Context)`，注册为 `GET /version`，返回 `{"version": "<version>"}`；启动日志打印版本号

- [ ] **Step 1: 写失败测试**

Create `balance_connector/main_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/version", getVersion)

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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd "C:\Users\andrew.yip\Projects\balance_connector" && "/c/Program Files/Go/bin/go.exe" test ./balance_connector/`
Expected: 编译失败：`undefined: getVersion`

- [ ] **Step 3: 最小实现**

在 `balance_connector/main.go` 的 `import` 中加入 `"log"`；在 `var (...)` 块中加入 `version = "dev"`；在 `main()` 的 `router.GET("/close", close)` 后加 `router.GET("/version", getVersion)`；在 `router.Run` 前加启动日志。文件末尾加 handler：

```go
func getVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": version})
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `"/c/Program Files/Go/bin/go.exe" test ./balance_connector/ -v`
Expected: `--- PASS: TestGetVersion`

- [ ] **Step 5: 实测 ldflags 注入（钉住 Review Focus #4）**

```bash
cd "C:\Users\andrew.yip\Projects\balance_connector"
"/c/Program Files/Go/bin/go.exe" build -ldflags "-s -w -X main.version=9.9.9-test" -o build/ ./balance_connector
./build/balance_connector.exe &
sleep 2
curl -s http://localhost:8333/version
# 期望输出: {"version":"9.9.9-test"}
taskkill //F //IM balance_connector.exe
```

Expected: `/version` 返回 `9.9.9-test`（而非 `dev`），证明注入链路可用。

- [ ] **Step 6: Commit**

```bash
git add balance_connector/main.go balance_connector/main_test.go
git commit -m "feat: add version variable (ldflags-injectable) and GET /version endpoint"
```

---

### Task 2: installer.wxs（替代 wix.json + go-msi）

**Files:**
- Create: `installer.wxs`（仓库根目录，与 `build/` 同级，Source 相对路径成立）
- Delete: `distribution/wix.json`、`distribution/build.bat`（go-msi 版）
- Modify: `balance_connector/main.go`（无需改，本任务纯打包配置）

**Interfaces:**
- Consumes: `build/balance_connector.exe`（Task 1 产物）、`build/nssm.exe`（CI 或本地脚本放入）
- Produces: 可执行 `wix build installer.wxs -d Version=<x.y.z> -out build/balance_connector-<version>.msi` 生成 MSI；定义的自定义动作 ID：`SvcInstall`/`SvcStart`/`SvcStop`/`SvcRemove`（后续任务不依赖，仅供评审）

- [ ] **Step 1: 删除 go-msi 遗物**

```bash
cd "C:\Users\andrew.yip\Projects\balance_connector"
git rm distribution/wix.json distribution/build.bat
```

- [ ] **Step 2: 写 installer.wxs**

```xml
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="balance_connector"
           Manufacturer="Pandaaaa906"
           Version="$(var.Version)"
           UpgradeCode="db86ffd8-a4a2-4c4d-8140-c31ff42c9cf7"
           Scope="perMachine">

    <MajorUpgrade DowngradeErrorMessage="A newer version of balance_connector is already installed." />
    <MediaTemplate EmbedCab="yes" />

    <Feature Id="Main">
      <ComponentGroupRef Id="MainComponents" />
    </Feature>

    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLDIR" Name="balance_connector" />
    </StandardDirectory>

    <Property Id="NSSM_EXE" Value="[INSTALLDIR]nssm.exe" />

    <ComponentGroup Id="MainComponents" Directory="INSTALLDIR">
      <Component Id="AppExe" Guid="*">
        <File Id="balance_connector.exe" Source="build/balance_connector.exe" KeyPath="yes" />
        <!-- 升级/卸载前停止服务：MajorUpgrade 卸载旧版时旧产品执行此 Stop -->
        <ServiceControl Id="SvcStopOnUninstall" Name="balance_connector" Stop="uninstall" Wait="yes" />
      </Component>
      <Component Id="NssmExe" Guid="*">
        <File Id="nssm.exe" Source="build/nssm.exe" KeyPath="yes" />
      </Component>
    </ComponentGroup>

    <CustomAction Id="SvcInstall" Property="NSSM_EXE" Execute="deferred" Return="check"
                  ExeCommand="install balance_connector &quot;[INSTALLDIR]balance_connector.exe&quot;" />
    <CustomAction Id="SvcStart" Property="NSSM_EXE" Execute="deferred" Return="ignore"
                  ExeCommand="start balance_connector" />
    <CustomAction Id="SvcStop" Property="NSSM_EXE" Execute="deferred" Return="ignore"
                  ExeCommand="stop balance_connector" />
    <CustomAction Id="SvcRemove" Property="NSSM_EXE" Execute="deferred" Return="ignore"
                  ExeCommand="remove balance_connector confirm" />

    <InstallExecuteSequence>
      <!-- 卸载（含升级时旧产品的卸载）：先停后删，再移除文件 -->
      <Custom Action="SvcStop" Before="RemoveFiles"><![CDATA[REMOVE="ALL"]]></Custom>
      <Custom Action="SvcRemove" After="SvcStop"><![CDATA[REMOVE="ALL"]]></Custom>
      <!-- 全新安装（升级场景下旧产品已卸载，NOT Installed 成立）：装服务并启动 -->
      <Custom Action="SvcInstall" After="InstallFiles"><![CDATA[NOT Installed]]></Custom>
      <Custom Action="SvcStart" After="SvcInstall"><![CDATA[NOT Installed]]></Custom>
    </InstallExecuteSequence>
  </Package>
</Wix>
```

要点（评审者须知）：
- `Property="NSSM_EXE"` + `Execute="deferred"`：MSI 引擎在执行时展开 `NSSM_EXE`（=[INSTALLDIR]nssm.exe）和 ExeCommand 中的 `[INSTALLDIR]`——这是 exe 类型 deferred CA 的标准用法。
- `Return="ignore"` 用在 stop/start/remove：服务不存在时 nssm 返回非零，不应阻断卸载。
- `MajorUpgrade` 默认调度（卸载旧产品在新产品装文件之前）保证升级时文件替换前服务已停、旧服务已移除。
- `ServiceControl Stop="uninstall"`：服务由 nssm 注册、WiX 不知情，必须按服务名显式停。

- [ ] **Step 3: 本地安装 WiX 并实测编译**

```bash
# 需本机有 .NET SDK（dotnet --version 验证）
dotnet tool install --global wix
# 准备 nssm 物料
"/c/Program Files/Go/bin/go.exe" build -ldflags "-s -w -X main.version=0.0.3" -o ./build/ ./balance_connector
curl -fL -o nssm.zip https://nssm.cc/release/nssm-2.24.zip
tar -xf nssm.zip
cp nssm-2.24/win64/nssm.exe build/nssm.exe
# 编译（bash 里先找到 wix.exe；若 dotnet tool 路径不在 PATH：export PATH="$PATH:$HOME/.dotnet/tools"）
wix build installer.wxs -d Version=0.0.3 -out build/balance_connector-0.0.3.msi
```

Expected: 生成 `build/balance_connector-0.0.3.msi`，无 error。如果本机无 dotnet SDK，跳过本步，由 Task 3 推 rc tag 在 CI 验证（在计划评审时注明）。

- [ ] **Step 4: Commit**

```bash
git add installer.wxs
git commit -m "packaging: replace go-msi/wix.json with WiX v4 installer.wxs (nssm service lifecycle)"
```

---

### Task 3: GitHub Actions 发布流水线

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `installer.wxs`（Task 2）、`build/balance_connector` 包（Task 1 的 version 变量）
- Produces: 推 `v*` tag 即生成 GitHub Release，资产为 `balance_connector-<tag去v>.msi`；`workflow_dispatch` 手动跑产出 MSI artifact（不打 Release）

- [ ] **Step 1: 写 workflow**

```yaml
name: release

on:
  push:
    tags: ["v*"]
  workflow_dispatch:

permissions:
  contents: write

jobs:
  release:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Resolve version from tag
        id: ver
        shell: pwsh
        run: |
          $tag = $env:GITHUB_REF_NAME
          if ($env:GITHUB_REF_TYPE -ne 'tag') { $tag = 'v0.0.0-dev' }
          $version = $tag.TrimStart('v')
          $msiVersion = ($version -split '-')[0]
          "tag=$tag" >> $env:GITHUB_OUTPUT
          "version=$version" >> $env:GITHUB_OUTPUT
          "msi_version=$msiVersion" >> $env:GITHUB_OUTPUT

      - name: Build exe
        shell: pwsh
        run: |
          go build -ldflags "-s -w -X main.version=${{ steps.ver.outputs.version }}" -o build/ ./balance_connector

      - name: Stage nssm (win64)
        shell: pwsh
        run: |
          Invoke-WebRequest -Uri "https://nssm.cc/release/nssm-2.24.zip" -OutFile nssm.zip -MaximumRetryCount 3
          Expand-Archive nssm.zip -DestinationPath nssm-extract
          Copy-Item nssm-extract/nssm-2.24/win64/nssm.exe build/nssm.exe

      - name: Install WiX
        shell: pwsh
        run: dotnet tool install --global wix

      - name: Build MSI
        shell: pwsh
        run: |
          wix build installer.wxs `
            -d Version=${{ steps.ver.outputs.msi_version }} `
            -out "build/balance_connector-${{ steps.ver.outputs.version }}.msi"

      - name: Upload MSI artifact
        uses: actions/upload-artifact@v4
        with:
          name: balance_connector-${{ steps.ver.outputs.version }}
          path: build/*.msi

      - name: Publish GitHub Release
        if: startsWith(github.ref, 'refs/tags/')
        uses: softprops/action-gh-release@v2
        with:
          files: build/*.msi
          generate_release_notes: true
```

要点：版本解析（`TrimStart('v')` + 按 `-` 截断得 MSI 版本）覆盖 Review Focus #3；`workflow_dispatch` 时 ref_type 为 branch，版本回落 `0.0.0-dev`/`0.0.0`，只出 artifact 不发 Release；`upload-artifact` 让手动调试也有产物可下载。

- [ ] **Step 2: YAML 语法静态检查**

Run: `python -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml', encoding='utf-8'))"`（或任意 YAML 解析器）
Expected: 无异常。注：`on:` 键在 YAML 1.1 解析为 True 不影响 GitHub（GitHub 用自家解析器），只要文件能被解析即可。

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add tag-driven release workflow (build exe + nssm MSI + GitHub Release)"
```

---

### Task 4: 本地安装包构建脚本

**Files:**
- Create: `distribution/build-installer.bat`

**Interfaces:**
- Consumes: `installer.wxs`（Task 2）、`build/balance_connector`（version 变量，Task 1）
- Produces: 双击或 `distribution\build-installer.bat [x.y.z]` 在本地复现 CI 的 MSI 构建（需本机装 WiX：`dotnet tool install --global wix`）

- [ ] **Step 1: 写脚本**

```bat
@echo off
REM 本地构建 MSI（需先: dotnet tool install --global wix）
REM 用法: distribution\build-installer.bat [版本号，默认 0.0.1]
set VERSION=%~1
if "%VERSION%"=="" set VERSION=0.0.1

if not exist build mkdir build
go build -ldflags "-s -w -X main.version=%VERSION%" -o ./build/ ./balance_connector

if not exist build\nssm.exe (
    echo downloading nssm...
    curl -fL -o nssm.zip https://nssm.cc/release/nssm-2.24.zip
    tar -xf nssm.zip
    copy /y nssm-2.24\win64\nssm.exe build\nssm.exe
)

wix build installer.wxs -d Version=%VERSION% -out "build\balance_connector-%VERSION%.msi"
echo MSI: build\balance_connector-%VERSION%.msi
```

- [ ] **Step 2: 若 Task 2 Step 3 已装 WiX，实测运行**

Run: `cmd //c ".\\distribution\\build-installer.bat 0.0.3"`
Expected: 输出 `MSI: build\balance_connector-0.0.3.msi`，文件存在。

- [ ] **Step 3: Commit**

```bash
git add distribution/build-installer.bat
git commit -m "packaging: add local build-installer.bat (WiX, mirrors CI)"
```

---

### Task 5: 文档更新 + 发布验证门

**Files:**
- Modify: `README.md`（新增发布流程章节）
- Modify: `CLAUDE.md`（构建/发布命令、删除 go-msi 描述、新增文件说明）

**Interfaces:**
- Consumes: 前面所有任务产物
- Produces: 发布操作文档；正式 tag 推送前的最终检查

- [ ] **Step 1: README.md 追加发布章节**

在文件末尾追加：

```markdown
### 发布流程

1. 确保所有变更已提交到 master。
2. 打 tag 并推送：`git tag v0.0.3 && git push origin v0.0.3`
3. GitHub Actions（`.github/workflows/release.yml`）自动完成：构建 exe（注入版本号）→ 打包内嵌 nssm 的 MSI → 创建 GitHub Release。
4. 在 Releases 页面下载 `balance_connector-<版本>.msi`，安装即注册并启动 `balance_connector` Windows 服务（监听 localhost:8333）。

本地手动构建 MSI（需 `dotnet tool install --global wix`）：`distribution\build-installer.bat 0.0.3`
```

- [ ] **Step 2: 更新 CLAUDE.md**

修改要点：
- 「常用命令」代码块中 `go build` 一行改为 `./build.bat`（产物 `./build/balance_connector.exe`，已有 -s -w）；补一行 `distribution\build-installer.bat <版本>` 说明
- 「发布」bullet 改为：tag 驱动，见 `.github/workflows/release.yml`（windows-latest：build → 下载 nssm-2.24.zip 取 win64 → `dotnet tool install -g wix` → `wix build installer.wxs` → GitHub Release）；删除 go-msi 与 `wix.json` 硬编码路径的警告（wix.json 已删）
- 「架构」首段：`installer.wxs`（WiX v4/v5，per-machine MSI，安装/升级/卸载时用 nssm 管理 balance_connector 服务）

- [ ] **Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: document tag-driven release process, update build commands"
```

- [ ] **Step 4: 手动验证门（发布 0.0.3 前的硬性检查，不可跳过）**

1. 推预发布 tag：`git tag v0.0.3-rc1 && git push origin v0.0.3-rc1`，确认 Actions 全绿、Release `v0.0.3-rc1` 出现且含 MSI
2. 找一台 Windows 测试机：装 rc MSI → `sc query balance_connector` 服务存在且 RUNNING → `curl http://localhost:8333/version` 返回 `0.0.3-rc1` → 控制面板卸载 → 服务消失（覆盖 Review Focus #1/#2）
3. 若机器上已有 0.0.2：直接装 rc MSI，验证升级路径（自动停旧服务、替换、起新服务，无重启提示）
4. 全部通过后推正式 tag `v0.0.3`，并在真机复验一遍

---

## Self-Review 记录

- Spec 覆盖检查：版本管理（Task 1）、规范化（Task 3 workflow）、流水线（Task 3）、installer.wxs 含服务生命周期/UpgradeCode/相对路径（Task 2）、本地脚本（Task 4）、删除 go-msi（Task 2）、文档（Task 5）、验证策略 rc→正式（Task 5 Step 4）、已知限制（Review Focus #5）。spec「执行前置」中 gin 定版已在计划开始前完成（v1.12.0 已恢复并构建通过）；「先 commit」= Task 0。
- 占位符扫描：无 TBD；所有代码步骤含完整代码。
- 一致性：`version` 变量、`getVersion`、自定义动作 ID、文件名 `balance_connector-<version>.msi` 在 Task 1/2/3/4 间一致。
- Review Focus 五项均有归属：#1/#2→Task 5 真机验证；#3→Task 3 步骤+rc 验证；#4→Task 1 Step 5；#5→workflow 重试+spec 已知限制。
