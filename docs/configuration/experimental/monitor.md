!!! question "Since sing-box 1.8.0 (monitor: this fork only)"

### Structure

```json
{
  "max_size_mb": 100,
  "max_dns_rows": 200000,
  "max_conn_rows": 100000,
  "retention_days": 7
}
```

### Fields

All fields are optional: `0` keeps the default, a negative value disables that cap, a positive value
overrides it.

#### max_size_mb

Maximum size of the monitoring database (`monitor.db`) in megabytes.

When the cap is reached the oldest records are deleted first — both by the periodic maintenance pass
and by SQLite itself, which refuses to grow the file past the cap even during a write burst.

`100` is used by default (100 MB); a negative value disables the cap.

#### max_dns_rows

Maximum number of DNS records kept in the monitoring database.

Oldest records are dropped first. `200000` is used by default; a negative value disables the cap.

#### max_conn_rows

Maximum number of connection records kept in the monitoring database.

Oldest records are dropped first. `100000` is used by default; a negative value disables the cap.

#### retention_days

Delete records older than this many days when the service starts.

`7` is used by default; a negative value disables time-based retention.
