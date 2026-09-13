# Log

### Structure

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

### Fields

#### disabled

Disable logging, no output after start.

#### level

Log level. One of: `trace` `debug` `info` `warn` `error` `fatal` `panic`.

#### output

Output file path. Will not write log to console after enable.

#### timestamp

Add time to each line.

#### max_size_mb

Maximum size of the log file in megabytes, applied when `output` is a file path.

When the cap is reached the oldest lines are dropped first (circular overwrite), so the file never grows
past the cap and the most recent logs are always available.

`100` is used by default (100 MB); a negative value disables the cap.
