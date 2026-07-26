# Monitoring Architecture (experimental/monitor/)

## Data Flow

```
routeConnection()                         Clash API poll
  │                                         │
  ├─ inject DialMeta{ConnID,Domain,Outbound} into ctx
  │                                         │
  ├─ outbound.NewConnection(ctx, ...)       │
  │   │                                     │
  │   ├─ resolveDialer.DialContext          │
  │   │   ├─ DNS lookup (d.router.Lookup)   │
  │   │   └─ dialTCP() ─── RecordTCP ──→ connMap[connID] ──→ SQLite INSERT ON CONFLICT(conn_id)
  │   │                                      │                    │
  │   ├─ DefaultDialer.DialContext           │                    │
  │   │   └─ RecordTCP (fallback)  ─────────┘                    │
  │   │                                                          │
  │   └─ tls.ClientHandshake                                     │
  │       └─ RecordTLS ──→ connMap[connID] ──→ SQLite UPDATE    │
  │                                                              │
  └─ trafficcontrol.RoutedConnection                             │
      └─ TrackerMetadata.ConnID ← DialMetaFromContext(ctx)       │
         └─ connectionsSnapshot()                                │
            └─ SyncTraffic(conn_id, traffic, outbound, ...) ─────┘
                                          SQLite INSERT ON CONFLICT(conn_id)
```

## Key Files

| File | Purpose |
|------|---------|
| `context.go` | `DialMeta` struct + context helpers |
| `types.go` | `DNSRecord`, `TCPRecord`, `TLSRecord`, `ConnectionRecord` |
| `collector.go` | `RecordDNS/RecordTCP/RecordTLS`, connMap (key=ConnID), `SyncTraffic` |
| `database.go` | SQLite schema, async batch writer, `WriteLatency` |
| `ringbuf.go` | In-memory ring buffer for recent events |
| `storage.go` | File-based persistence |
| `api.go` | HTTP API for querying history |

## SQLite Schema

`connection_records` — per-connection rows, keyed by `conn_id` (UNIQUE).

Each connection produces up to 3 writes targeting the same row:
1. TCP hook → `WriteLatency(conn_id, tcp_latency, 0)` → INSERT
2. TLS hook → `WriteLatency(conn_id, 0, tls_latency)` → ON CONFLICT UPDATE
3. traffic_sync → `SyncTraffic(conn_id, upload, download, outbound)` → ON CONFLICT UPDATE

## Hook Placement Rationale

### Why resolveDialer for TCP?
DefaultDialer.DialContext is NOT reached for direct outbound domain connections. The resolveDialer wraps DefaultDialer and always intercepts domain destinations:
1. DNS resolution
2. TCP connect via N.DialSerial/N.DialParallel

Hooking in `resolveDialer.dialTCP()` (after DNS) covers ALL paths.

### Why not outbound-level TLS?
`common/tls/client.go` only captures TLS when ConnectionManager wraps connections. VLESS+Reality, VMess TLS, SOCKS5-TLS all handle TLS internally — not captured.

### Why context propagation?
Dialer hooks have no outbound info. Domain + outbound tag + unique connID are injected into ctx at `route/route.go:routeConnection()` before the outbound is called, then extracted by hooks via `monitor.DialMetaFromContext(ctx)`.

## connMap Details

Key = `r.ConnID` (e.g., "google.com-1750150876123456789"), fallback to `r.Remote` (IP:port).

Each new connection gets a unique ConnID (timestamp-based) → no overwrite between multiple connections to same destination.
