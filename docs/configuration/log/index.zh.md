# 日志

### 结构

```json
{
  "log": {
    "disabled": false,
    "level": "info",
    "output": "box.log",
    "timestamp": true
  }
}
```

### 字段

#### disabled

禁用日志，启动后不输出日志。

#### level

日志等级，可选值：`trace` `debug` `info` `warn` `error` `fatal` `panic`。

#### output

输出文件路径，启动后将不输出到控制台。

日志文件上限固定为 100 MB：达到上限后优先丢弃最旧的日志行（循环覆盖写），文件不会超过该上限，
且始终保留最近的日志。该上限是引擎内的固定常量，不可配置。

#### timestamp

添加时间到每行。
