!!! question "Since sing-box 1.8.0 (monitor: this fork only)"

### Structure

```json
{
  "max_dns_rows": 2000,
  "max_conn_rows": 1000,
  "fail_dedupe_sec": 60
}
```

### Fields

All fields are optional: `0` keeps the default, a positive value overrides it.

Records are kept **in memory only** — two fixed-size ring buffers, no database file. Nothing is
written to disk, and the history is empty again after a restart. Once a ring is full the oldest
record is overwritten; how many records were overwritten is reported as `dropped` by
`/monitor/stats` and `/monitor/health`.

A client reads at most 1000 records at a time, so the ring capacity decides how far back the
newest records reach — not how many records exist.

#### max_dns_rows

Number of DNS records kept in memory. `2000` is used by default. `0` and negative values use the
default (a ring buffer must be bounded); values above `50000` are clamped to `50000`.

#### max_conn_rows

Number of connection records kept in memory. `1000` is used by default. `0` and negative values use
the default; values above `20000` are clamped to `20000`.

#### fail_dedupe_sec

Window for merging repeated failures: within the window, repeated failures for the same
destination (connections: `remote` + `outbound`; DNS: `domain` + `qtype` + `status`) are recorded
only once.

A single upstream outage produces a burst of failures, and those bursts are what flush useful
history out of the ring. `60` is used by default; a negative value disables deduplication.

#### Deprecated fields

`max_size_mb`, `retention_days` and `maintenance_log_sec` configured the on-disk database. They are
still accepted — so existing configuration files keep working — but they no longer have any effect.
