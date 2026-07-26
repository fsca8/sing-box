# 源码调试 Patch — sing-box 1.13.x

## common/tls/client.go — TCP vs TLS 分段计时

### 添加 import
```go
import (
    ...
    "fmt"
    "time"
    ...
)
```

### 修改 dialContext()
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
        var echErr *tls.ECHRejectionError
        if echRetry && errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
            if echConfig, isECH := d.config.(ECHCapableConfig); isECH {
                echConfig.SetECHConfigList(echErr.RetryConfigList)
                return d.dialContext(ctx, destination, false)
            }
        }
        return nil, err
    }
    t2 := time.Now()
    fmt.Printf("[DEBUG] TCP+TLS handshake to %s: total=%v (TCP=%v, TLS=%v)\n",
        destination, t2.Sub(t0), t1.Sub(t0), t2.Sub(t1))
    return tlsConn, nil
}
```

## common/tls/reality_client.go — Reality 分阶段计时

```go
func (e *RealityClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
    t0 := time.Now()
    // ... existing code ...
    err = uConn.BuildHandshakeState()
    // ... existing code ...
    t1 := time.Now()
    // filter curves/shares...
    err = uConn.BuildHandshakeState()
    t2 := time.Now()
    // SessionId setup...
    t3 := time.Now()
    // ECDH + AES-GCM...
    t4 := time.Now()
    err = uConn.HandshakeContext(ctx)
    t5 := time.Now()

    fmt.Printf("[REALITY] build_state=%v filter+rebuild=%v session_prep=%v ecdh+crypt=%v handshake=%v total=%v\n",
        t1.Sub(t0), t2.Sub(t1), t3.Sub(t2), t4.Sub(t3), t5.Sub(t4), t5.Sub(t0))

    // ... rest of existing code ...
}
```

## 编译调试版

```bash
# 克隆仓库
git clone --depth 1 --branch v1.13.14 https://github.com/SagerNet/sing-box.git
cd sing-box

# 应用 patch（手动编辑上述文件）
# common/tls/client.go
# common/tls/reality_client.go

# 编译
go build -v -tags "with_utls,with_gvisor" -o sing-box-debug.exe ./cmd/sing-box
```
