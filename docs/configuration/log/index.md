# Log

### Structure

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

### Fields

#### disabled

Disable logging, no output after start.

#### level

Log level. One of: `trace` `debug` `info` `warn` `error` `fatal` `panic`.

#### output

Output file path. Will not write log to console after enable.

The file is capped at 100 MB: when the cap is reached the oldest lines are dropped first (circular
overwrite), so the file never grows past the cap and the most recent logs are always available. The cap
is a fixed constant and not configurable.

Rotation keeps half of the cap (50 MB, the newest part), so the file size cycles between 50% and 100% of
the cap. A smaller retained window means fewer bytes to shift in place (write amplification ≈2× instead
of ≈4×) — a deliberate trade-off for SSD wear.

#### timestamp

Add time to each line.
