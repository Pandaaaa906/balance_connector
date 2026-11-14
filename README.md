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
    "msg": ""
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
#### Response 200
```yaml
{
    "time": "2022-08-22T11:49:39.8697861+08:00",
    "data": "    0.1818g \r"
}
```

### GET /close
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
### 发布流程

1. 确保所有变更已提交到 master。
2. 打 tag 并推送：`git tag v0.0.3 && git push origin v0.0.3`
3. GitHub Actions（`.github/workflows/release.yml`）自动完成：构建 exe（`-ldflags` 注入版本号）→ 下载 nssm-2.24 打包 MSI → 创建 GitHub Release。
4. 在 Releases 页面下载 `balance_connector-<版本>.msi`。安装即注册并启动 `balance_connector` Windows 服务（监听 localhost:8333），卸载自动停止并移除服务；升级安装会自动替换旧版。

首次发布建议先推预发布 tag（如 `v0.0.3-rc1`）在测试机验证安装/升级/卸载后再推正式 tag。

本地手动构建 MSI（需先 `dotnet tool install --global wix --version 5.0.2`）：`distribution\build-installer.bat 0.0.3`
