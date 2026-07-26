# sing-box Debugging Guide

## Source Code Timing Instrumentation

### File 1: `common/tls/client.go`

Add imports:
```go
"fmt"
"time"
```

In `dialContext()` function, wrap TCP dial and TLS handshake:

```go
func (d *defaultDialer) dialContext(ctx context.Context, destination M.Socksaddr, echRetry bool) (Conn, error) {
    t0 := time.Now()
    conn, err := d.dialer.DialContext(ctx, N.NetworkTCP, destination)
    if err != nil {
        return nil, err
    }
    t1 := time.Now()
    fmt.Printf("[DEBUG] TCP dial to %s took %v\n", destination, t1.Sub(t0))
    tlsConn, err := aTLS.ClientHandshake(ctx, conn, d.config)
    if err != nil {
        conn.Close()
        t2 := time.Now()
        fmt.Printf("[DEBUG] TLS handshake FAILED after %v (TCP was %v)\n", t2.Sub(t1), t1.Sub(t0))
        // ... ECH retry ...
        return nil, err
    }
    t2 := time.Now()
    fmt.Printf("[DEBUG] TCP+TLS handshake to %s: total=%v (TCP=%v, TLS=%v)\n",
        destination, t2.Sub(t0), t1.Sub(t0), t2.Sub(t1))
    return tlsConn, nil
}
```

### File 2: `common/tls/reality_client.go`

In `ClientHandshake()`, insert timing markers at each phase:

```go
t0 := time.Now()
// ... uTLS setup ...
err := uConn.BuildHandshakeState()  // first build
// ...
t1 := time.Now()
// ... filter X25519MLKEM768 curves ...
err = uConn.BuildHandshakeState()  // second build
t2 := time.Now()
// ... session ID preparation ...
t3 := time.Now()
// ... ECDH + AES-GCM encryption ...
t4 := time.Now()
// ... HandshakeContext ...
t5 := time.Now()

fmt.Printf("[REALITY] build_state=%v filter+rebuild=%v session_prep=%v ecdh+crypt=%v handshake=%v total=%v\n",
    t1.Sub(t0), t2.Sub(t1), t3.Sub(t2), t4.Sub(t3), t5.Sub(t4), t5.Sub(t0))
```

## Connection Flow Summary

```
vlessDialer.DialContext()
  ├─ h.tlsDialer.DialTLSContext(h.serverAddr)      ← outbound.go:154
  │   └─ d.dialer.DialContext(TCP, proxy:port)      ← client.go:118 (timeout=5s)
  │   └─ RealityClientConfig.ClientHandshake()       ← reality_client.go:119
  │       ├─ utls.UClient()
  │       ├─ BuildHandshakeState() x2                 ← CPU
  │       ├─ ECDH + AES-GCM SessionId                 ← CPU
  │       └─ uConn.HandshakeContext()                 ← Network (2-3 RTT)
  └─ h.client.DialEarlyConn(conn, destination)       ← VLESS protocol
```

## Performance Baselines

| Metric | Expected | Problematic |
|--------|----------|-------------|
| TCP dial (RTT 200ms) | 200-400ms | >5s (timeout) |
| uTLS BuildHandshakeState | <5ms | >50ms |
| ECDH + AES | <1ms | >10ms |
| HandshakeContext | 400-800ms | >5s |
| Total per connection | 0.6-1.2s | 5-18s |

## Common Errors

| Error | Root Cause | Fix | Diagnostic |
|-------|-----------|-----|------------|
| `UDP is not supported by outbound: proxy` | DNS UDP traffic hits proxy outbound with `network: tcp` | Add `protocol: dns → direct` route rule | Check singbox.log for ERROR |
| `ERR_NAME_NOT_RESOLVED` | DNS loop from strict_route | `strict_route: false` + `route_exclude_address` | Browser error, nslookup works |
| `i/o timeout` dialing proxy | Server SYN backlog full | Server-side fix or reduce concurrent connections | debug binary shows TCP dial >5s |
| `operation was canceled` | Browser canceled waiting | Slow server | debug binary shows long handshake |

## Diagnostic Flow

### Step 1: Source timing (if you can compile)

Build debug binary:
```bash
cd sing-box-source
git clone --depth 1 --branch v1.13.14 https://github.com/SagerNet/sing-box.git
# Apply timing patches to common/tls/client.go and common/tls/reality_client.go
go build -tags "with_utls,with_gvisor" -o sing-box-debug.exe ./cmd/sing-box
```

Run and look for lines like:
```
[DEBUG] TCP dial to SERVER:PORT took 222ms
[REALITY] build_state=1ms filter+rebuild=0s session_prep=0s ecdh+crypt=0s handshake=1.07s total=1.08s
[DEBUG] TCP+TLS handshake to SERVER:PORT: total=1.3s (TCP=222ms, TLS=1.07s)
```

### Step 2: Protocol swap

If step 1 shows normal timings but actual browsing is slow, switch to VMess (no TLS/Reality):
- Server: add VMess inbound on separate port
- Client: replace outbound with VMess
- Compare connection establishment and page load times

### Step 3: TUN vs SOCKS5

If still slow, switch from TUN inbound to mixed (SOCKS5+HTTP) inbound:
- Server config unchanged
- Client: replace TUN with mixed inbound
- Test via: `curl -x socks5://127.0.0.1:2080 https://www.google.com/generate_204`
- If SOCKS5 is fast but TUN is slow → Windows TUN driver issue

### Step 4: Server-side network check

Log into the proxy server and verify:
```bash
ping -c 10 8.8.8.8           # should be <1ms in US DC
curl speed.cloudflare.com/__down?bytes=10485760  # should be >10MB/s
iptables -L -n -v | grep limit  # check rate limiting
ip -s link                    # check interface errors
```
