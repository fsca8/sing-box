# 日志

### 结构

```json
{
  "log": {
    "disabled": false,
    "level": "info",
    "output": "box.log",
    "timestamp": true,
    "max_size_mb": 100
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

#### timestamp

添加时间到每行。

#### max_size_mb

日志文件的最大大小（MB），仅在 `output` 为文件路径时生效。

达到上限后优先丢弃最旧的日志行（循环覆盖写），文件不会超过该上限，且始终保留最近的日志。

默认使用 `100`（100 MB），负数表示不限制。
