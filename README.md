## Balance Connector
## 天平连接插件


### POST /open 
```yaml
{
    "port": "COM6",  # 必填
    "baudrate": 1200,  # 可不填, 默认1200
    "databits": 8,  # 可不填, 默认8
    "parity": "N",  # 可不填, 默认"N", 可选 "N", "O", "E", "M", "S"
    "stopbits": "1",  # 可不填, 默认"1", 可选"1", "1.5", "2"
    "expected": "\r", # 可不填, 默认"\r"
    "timeout": 5  # 可不填, 默认5秒
}
```
#### Response 200
```yaml
{
    "status": "success",
    "msg": "",
    "session": "a1b2c3d4e5f60708"   # 0.0.4 起新增；同时通过 Set-Cookie 种下 bc_session
}
```
#### Response 400
```yaml
{
    "status":"failed",
    "msg":"Key: 'serialOpenArgs.Parity' Error:Field validation for 'Parity' failed on the 'validateParityChoice' tag"
}
```
#### Response 500
```yaml
{
    "status": "failed",
    "msg": "A device which does not exist was specified."  # 会有不一样
}
```

### GET /read
按 session 路由，规则见"多 session"一节。
#### Response 200
```yaml
{
    "time": "2022-08-22T11:49:39.8697861+08:00",
    "data": "    0.1818g \r"
}
```

### GET /close
按 session 路由，规则见"多 session"一节。
#### Response 200
```yaml
{
    "status": "success",
    "msg": ""
}
```
#### Response 500
```yaml
{
    "status": "failed",
    "msg": "port is not opened"
}
```

### 多 session（0.0.4 起）

服务可同时连接多台天平，每台对应一个 session：

- `POST /open` 返回 `session` 字段并种下 cookie `bc_session`（浏览器会话期）
- `GET /read`、`GET /close` 的 session 路由优先级：`?session=<id>` 查询参数 > `bc_session` cookie > **default_session_key**（最早 open 且仍存活的 session；被 close 后自动顺延到下一个）
- 旧客户端不带 cookie/参数时自动走 default_session_key，行为与单秤版一致
- 同一 COM 口重复 open 返回 500 `port is opened`
- 多 tab 同时读多台秤时，因 cookie 按站点共享，请各自拼 `?session=` 参数
### 发布流程

1. 确保所有变更已提交到 master。
2. 打 tag 并推送：`git tag v0.0.3 && git push origin v0.0.3`
3. GitHub Actions（`.github/workflows/release.yml`）自动完成：构建 exe（`-ldflags` 注入版本号）→ 下载 nssm-2.24 打包 MSI → 创建 GitHub Release。
4. 在 Releases 页面下载 `balance_connector-<版本>.msi`。安装即注册并启动 `balance_connector` Windows 服务（监听 localhost:8333），卸载自动停止并移除服务；升级安装会自动替换旧版。

首次发布建议先推预发布 tag（如 `v0.0.3-rc1`）在测试机验证安装/升级/卸载后再推正式 tag。

本地手动构建 MSI（需先 `dotnet tool install --global wix --version 5.0.2`）：`distribution\build-installer.bat 0.0.3`
