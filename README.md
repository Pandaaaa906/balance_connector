## Balance Connector
## 天平连接插件


### POST /open 
```yaml
{
    "port": "COM6",  # 必填
    "baudrate": 1200,  # 可不填, 默认1200
    "databits": 8,  # 可不填, 默认8
    "parity": 0,  # 可不填, 默认0
    "stopbits": 0,  # 可不填, 默认0
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