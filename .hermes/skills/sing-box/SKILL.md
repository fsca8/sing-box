---
name: sing-box
description: Configure and troubleshoot sing-box TUN proxy (VLESS+Reality+uTLS) on Windows. Covers config validation, DNS routing, TUN performance issues, and version-specific pitfalls.
category: networking
triggers:
  - sing-box
  - sing-box configuration
  - sing-box TUN
  - VLESS Reality configuration
  - sing-box DNS not resolving
  - sing-box slow proxy
  - sing-box rule set deadlock
  - sing-box cache empty
  - sing-box clash api
  - sing-box monitoring
  - sing-box ad blocking
  - sing-box geosite
  - sing-box geoip split routing
  - sing-box DoH DNS
---

# sing-box TUN Proxy (Windows)

## Config Structure

Required JSON structure for sing-box v1.13+:

```json
{
  "log": { "level": "info", "output": "singbox.log", "timestamp": true },
  "dns": { "servers": [...], "strategy": "ipv4_only" },
  "inbounds": [{ "type": "tun", ... }],
  "outbounds": [{ "type": "vless", ... }, { "type": "direct" }, { "type": "block" }],
  "route": { "rules": [...], "rule_set": [...], "final": "proxy" }
}
```

## Version-Specific Pitfalls (v1.13+)

### 1. `sniff` field REMOVED from TUN inbound (critical!)

In sing-box 1.11.0 `sniff` was deprecated in TUN inbound; in 1.13.0 it was **removed entirely**. Configs containing `"sniff": true` inside the TUN inbound will fail with:

```
FATAL legacy inbound fields are deprecated in sing-box 1.11.0 and removed in sing-box 1.13.0
```

**Fix:** Remove `sniff` from the TUN inbound. Add `{"action": "sniff"}` as the first route rule instead:
```json
"inbounds": [{ "type": "tun", ... }],
"route": {
  "rules": [
    { "action": "sniff" },
    ...
  ]
}
```

### 2. `default_domain_resolver` required (1.12+)

Without it, sing-box 1.12+ will error:
```
missing route.default_domain_resolver or domain_resolver in dial fields
```

**Fix:** Add to route section:
```json
"route": {
  ...
  "default_domain_resolver": "alidns"
}
```

### 3. `detour: "direct"` on DNS servers is INVALID

In sing-box 1.13, this causes:
```
detour to an empty direct outbound makes no sense
```

**Fix:** Remove `detour` field from DNS server configs.

### 4. Reality REQUIRES uTLS

```
uTLS is required by reality client
```

`"utls": {"enabled": false}` is not allowed with Reality. Must use:
```json
"utls": { "enabled": true, "fingerprint": "chrome" }
```

## DNS Troubleshooting

### `ERR_NAME_NOT_RESOLVED` with TUN + `strict_route: true`

**Root cause:** `strict_route: true` creates WFP rules redirecting ALL DNS through TUN. The fake DNS (172.19.0.2) intercepts queries and forwards to upstream. Forwarded DNS packets go through the system stack, where WFP can re-capture them — creating a loop.

**Fix:** Set `strict_route: false` and explicitly exclude DNS server IPs:
```json
"inbounds": [{
  "type": "tun",
  "strict_route": false,
  "route_exclude_address": ["223.5.5.5/32", "1.12.12.12/32"]
}]
```
Add safety-net route rule:
```json
{ "protocol": "dns", "outbound": "direct" }
```

### `ERR_CONNECTION_CLOSED` with TUN + IPv6 + IPv4-only proxy

**Symptoms:** `nslookup` resolves successfully, but `curl` (without `-4`) fails with SSL error (exit 35). `curl -4` works fine. Browser shows `ERR_CONNECTION_CLOSED`. Log shows `outbound/vless[proxy]: EOF` on DNS or `dial tcp ... i/o timeout` for IPv6 destinations.

**Root cause:** TUN inbound has an IPv6 address (`fdfe:dcba:9876::1/126`), creating an IPv6 fake DNS endpoint. Applications resolve via IPv6 first, get AAAA records, then try IPv6 connections. If proxy server is IPv4-only, IPv6 connections fail.

**Fix:** Remove IPv6 address from TUN inbound:
```json
"inbounds": [{
  "type": "tun",
  "address": ["172.19.0.1/30"]
}]
```
Optionally add `"strategy": "ipv4_only"` to DNS config.

### DNS queries routed through proxy (`outbound/vless[proxy]: EOF`)

**Root cause:** `dns.final` is a DNS server with `detour: proxy` (e.g., 1.1.1.1 via VLESS). VLESS/UDP DNS queries get EOF from server.

**Fix:** Set `dns.final` to a local DNS server without proxy detour:
```json
"dns": { "final": "dns-direct" }
```
And ensure route rules have `hijack-dns` BEFORE `dns → direct`:
```json
"route": { "rules": [
  {"protocol": "dns", "action": "hijack-dns"},
  {"protocol": "dns", "outbound": "direct"}
]}
```
**Order matters:** if `dns → direct` is first, sing-box's DNS module (and its `strategy` setting) is completely bypassed.

## Performance Issues

### Slow proxy connections (5-18s vs expected 1-2s)

VLESS+Reality+uTLS connection flow:
```
TCP dial → uTLS BuildHandshakeState(x2) → ECDH → AES-GCM → TLS Handshake → VLESS
```

With 210ms RTT and 0% packet loss, expected ~1s. 5-18s indicates:

1. **TCP dial timeout (5s = TCPConnectTimeout):** Server dropping SYNs (rate limiting / SYN backlog full). Server-side issue.
2. **Stuck HandshakeContext:** Reality verification or server-side CPU saturation.

### Debugging with source timing

**Old approach (manual printf):**
- `common/tls/client.go` — split TCP dial and TLS handshake timing
- `common/tls/reality_client.go` — split BuildHandshakeState, ECDH, HandshakeContext
- Compile: `go build -tags "with_utls,with_gvisor" -o sing-box-debug.exe ./cmd/sing-box`

**New approach (monitor.Collector instrumentation):**

See `references/debug-timing.md` for the older manual printf approach. For a persistent monitoring system, the `experimental/monitor/` package provides `RecordDNS`, `RecordTCP`, `RecordTLS` hooks at:

1. **DNS** — `dns/client.go:exchangeToTransport()` / `exchangeToTransportAsync()`: real DNS wire calls (not cached)
2. **TCP** — `common/dialer/resolve.go:dialTCP()`: ALL outbound TCP connects (direct + proxy), timer starts AFTER DNS so latency is pure TCP. Also fallback in `DefaultDialer.DialContext` for paths that bypass the resolver.
3. **TLS** — `common/tls/client.go:dialContext()`: TLS handshake (note: does NOT capture outbound-level TLS like VLESS+Reality)

**Critical: TCP hook MUST be in resolveDialer, NOT DefaultDialer.** For direct outbound with domain destinations, the resolveDialer intercepts the call, resolves DNS, then calls `N.DialSerial`/`N.DialParallel`. If the dialer implements `ParallelDialer`, these may call `DialParallel` instead of `DialContext`, skipping DefaultDialer hooks entirely.

Connection context (domain, outbound tag, unique connID) flows via `monitor.DialMeta` stored in `context.Context`, injected at `route/route.go:routeConnection()`. The connID links the TCP hook, TLS hook, and Clash API `traffic_sync` to the same SQLite row.

## Building

Always use `dev.sh` from project root — it injects the correct version string (commit hash + build date) and required build tags (`with_utls,with_gvisor,with_clash_api`).

```bash
cd ~/works/sing-box

# Debug build (default, no stripping)
./dev.sh

# Release build (stripped, smaller binary)
./dev.sh release

# Custom tags
TAGS="with_utls,with_gvisor,with_clash_api,with_quic" ./dev.sh release
```

Output: `sing-box-<VERSION>.exe`

**BUILD DISCIPLINE:**
- Never `go clean -cache` — causes 80-90% CPU and 5+ minute rebuilds
- Never build without explicit user approval ("编译太慢，无明确指令绝对不能编译")
- CGO is required on Windows: `export PATH="/c/msys64/mingw64/bin:$PATH"`

## Protocol Debugging: Swap to VMess

To isolate whether slowness is Reality/Flow-specific vs pure server network:

**Server-side:** Add VMess inbound on different port:
```json
{
  "type": "vmess",
  "tag": "vmess-in",
  "listen": "::",
  "listen_port": 12398,
  "users": [{ "uuid": "<same-uuid>", "alter_id": 0 }]
}
```

**Client-side:** Replace VLESS outbound with VMess:
```json
{
  "type": "vmess",
  "tag": "proxy",
  "server": "SERVER_IP",
  "server_port": 12398,
  "uuid": "<uuid>",
  "security": "auto"
}
```

VMess uses far less overhead (no uTLS, no Reality, no Vision flow). If VMess is also slow, the issue is NOT protocol-specific.

## TUN vs SOCKS5 Mode Comparison

TUN mode adds packet-level processing overhead. To test if TUN itself is the bottleneck:

Replace TUN inbound with a mixed (SOCKS5+HTTP) proxy inbound:
```json
"inbounds": [{
  "type": "mixed",
  "tag": "mixed-in",
  "listen": "127.0.0.1",
  "listen_port": 2080
}]
```

Test via curl:
```bash
curl -s -o /dev/null -w "TCP=%{time_connect}s TLS=%{time_appconnect}s TOTAL=%{time_total}s SPEED=%{speed_download}B/s\n" \
  -x socks5://127.0.0.1:2080 \
  --connect-timeout 15 --max-time 30 \
  "https://www.google.com/generate_204"
```

If SOCKS5 mode is fast but TUN mode is slow, the issue is Windows TUN driver/stack performance. If both are slow, the issue is the proxy server or network path.

## Server-Side Network Diagnostics

When client-side debugging suggests the proxy server itself is slow, use the bundled script:

```bash
# Copy from Hermes skill directory to server
# Script located at: scripts/netcheck.sh (relative to this skill)
# Run on the server:
chmod +x netcheck.sh && sudo bash netcheck.sh
```

```bash
# 1. Basic server health
uptime
free -h

# 2. Port listening
ss -tlnp | grep -E "12399|12398"

# 3. Ping to key targets (10 packets)
ping -c 10 8.8.8.8      # Google
ping -c 10 149.154.167.41  # Telegram
ping -c 10 223.5.5.5    # China (AliDNS)

# 4. Bandwidth test (10MB download)
curl -s -o /dev/null -w "Speed: %{speed_download} B/s\n" \
  --connect-timeout 15 --max-time 30 \
  https://speed.cloudflare.com/__down?bytes=10485760

# 5. Actual HTTP download test from server
curl -s -o /dev/null -w "HTTP=%{http_code} TCP=%{time_connect}s TLS=%{time_appconnect}s TOTAL=%{time_total}s SPEED=%{speed_download}B/s\n" \
  --connect-timeout 15 --max-time 30 "https://www.google.com/generate_204"

# 6. Check firewall rate limiting
iptables -L -n -v | grep -i "limit\|connlimit"

# 7. Check network interface errors
ip -s link
```

### Key Metrics

| Server Metric | Healthy | Problematic |
|--------------|---------|-------------|
| ping to Google (8.8.8.8) | <1ms (same DC) | >10ms |
| ping to China (223.5.5.5) | 40-200ms | >300ms |
| Download speed (CF 10MB) | >10MB/s (80Mbps+) | <1MB/s |
| Server CPU | <50% | >80% |
| Interface RX errors | 0 | >0.1% of packets |

## GeoIP / Geosite Split Routing

Add rule sets for China/non-China traffic separation:

```json
"route": {
  "rule_set": [
    { "type": "remote", "tag": "geoip-cn", "format": "binary",
      "url": "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs",
      "download_detour": "proxy" },
    { "type": "remote", "tag": "geosite-cn", "format": "binary",
      "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
      "download_detour": "proxy" },
    { "type": "remote", "tag": "geosite-geolocation-!cn", "format": "binary",
      "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs",
      "download_detour": "proxy" }
  ],
  "rules": [
    { "action": "sniff" },
    { "protocol": "dns", "action": "hijack-dns" },
    { "rule_set": "geosite-geolocation-!cn", "outbound": "proxy" },
    { "rule_set": "geosite-cn", "outbound": "direct" },
    { "rule_set": "geoip-cn", "outbound": "direct" },
    { "protocol": "dns", "outbound": "direct" }
  ],
  "final": "proxy"
}
```

### Critical: SagerNet geoip-cn misclassifies Google IPs

SagerNet `geoip-cn.srs` (2026-07) incorrectly includes Google IP ranges (74.125.x.x, 108.177.x.x, 172.217.x.x). If `geoip-cn → direct` is placed BEFORE `geosite-geolocation-!cn → proxy`, Google connections go direct → timeout.

**Fix:** `geosite-geolocation-!cn → proxy` MUST precede `geoip-cn → direct`. Domain-based rules are more accurate than IP-based.

### DNS split

```json
"dns": {
  "strategy": "ipv4_only",
  "servers": [
    { "type": "udp", "tag": "dns-direct", "server": "223.5.5.5" },
    { "type": "https", "tag": "dns-remote", "server": "1.1.1.1", "path": "/dns-query", "detour": "proxy" }
  ],
  "rules": [{ "rule_set": "geosite-cn", "server": "dns-direct" }],
  "final": "dns-remote"
}
```

See also: references/split-routing-geoip.md (full template + verification commands)

## DNS Encryption (DoH / DoT)

To replace plaintext UDP DNS with encrypted DNS (recommended for privacy and anti-pollution):

### Alibaba DoH (`dns.alidns.com`)

```json
{
  "type": "https",
  "tag": "dns-direct",
  "server": "dns.alidns.com",
  "path": "/dns-query",
  "domain_resolver": "dns-remote"
}
```

### Cloudflare DoH (`1.1.1.1`)

```json
{
  "type": "https",
  "tag": "dns-remote",
  "server": "1.1.1.1",
  "path": "/dns-query",
  "detour": "proxy"
}
```

### Bootstrap pitfall: `domain_resolver` is REQUIRED for domain-name DoH servers

When a DNS server uses a **domain name** (not raw IP), sing-box must resolve that domain before it can connect. This creates a chicken-and-egg problem. The fix is `domain_resolver` — point it at another DNS server that uses a **raw IP**:

```
dns-direct (dns.alidns.com)  ──bootstrap──▶  dns-remote (1.1.1.1, raw IP)
                                               │
                                               └── resolves via proxy
```

Without `domain_resolver`, sing-box fails with:
```
FATAL initialize DNS server[0]: missing domain resolver for domain server address
```

### DoH vs UDP: route_exclude_address is NOT needed for DoH

DoH uses TCP/443 (HTTPS), not UDP/53. It goes through the normal routing path. The `route_exclude_address` TUN option only applies to plaintext UDP DNS servers that need to bypass TUN to avoid loops. When all DNS servers are DoH, `route_exclude_address` can be removed.

Common encrypted DNS providers:

| Provider | Type | Address | Bootstrap |
|----------|------|---------|-----------|
| Alibaba (AliDNS) | DoH | `dns.alidns.com/dns-query` | needs `domain_resolver` |
| Cloudflare | DoH | `1.1.1.1/dns-query` | raw IP, no bootstrap |
| Google | DoH | `8.8.8.8/dns-query` | raw IP, no bootstrap |
| DNSPod (Tencent) | DoH | `doh.pub/dns-query` | needs `domain_resolver` |

## Ad Blocking (DNS-level, no extra software)

Sing-box can block ad/tracking domains at the DNS level using `geosite-category-ads` — no AdGuard Home needed.

### Add to route.rule_set

```json
{
  "type": "remote",
  "tag": "geosite-category-ads",
  "format": "binary",
  "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads.srs",
  "download_detour": "proxy"
}
```

### Add to dns.rules (BEFORE geosite-cn)

```json
{ "rule_set": "geosite-category-ads", "action": "reject" }
```

`action: "reject"` returns "Query refused" — domains like `doubleclick.net`, `googleadservices.com`, `pagead2.googlesyndication.com` are blocked at DNS level. Normal domains are unaffected.

### Verification

```bash
nslookup doubleclick.net        # → Query refused (blocked)
nslookup baidu.com              # → resolves normally
```

## Rule Set Deadlock (type: remote + download_detour: proxy)

**Symptom:** sing-box FATALs on first start when cache is empty:
```
FATAL start service: initialize rule-set[0]: initial rule-set: geoip-cn:
Get "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs": EOF
```

**Root cause:** `type: remote` + `download_detour: proxy` creates a deadlock:
1. Rule sets are on GitHub (blocked in China, need proxy to download)
2. Proxy routing needs rule sets loaded → rule sets can't download → can't start → can't proxy
3. If cache was populated from a previous run it works; if cache is cleared, it's fatal

**Fix A — use `type: local` (recommended for stability):**
```bash
# Download .srs files manually via proxy
export https_proxy=http://127.0.0.1:10809
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs
# ...
```
```json
{ "type": "local", "tag": "geoip-cn", "format": "binary",
  "path": "C:\\Programs\\sing-box\\geoip-cn.srs" }
```
Use **absolute paths** — sing-box may run from a different working directory than expected.

**Fix B — keep remote, bootstrap via system proxy:**
Sing-box respects `HTTP_PROXY`/`HTTPS_PROXY` env vars for rule set downloads. Set these before starting sing-box.

## Clash API — Built-in Monitoring Endpoints

sing-box has a Clash-compatible REST API for real-time monitoring. Enable with:

```json
"experimental": {
  "clash_api": {
    "external_controller": "127.0.0.1:9090",
    "access_control_allow_origin": ["*"],
    "access_control_allow_private_network": true
  }
}
```

### Key endpoints

| Endpoint | Returns |
|----------|---------|
| `GET /connections` | All active connections: source/dest IP, domain, rule matched, upload/download bytes, start time, process |
| `GET /connections` (WS) | WebSocket streaming snapshots (`?interval=1000`) |
| `GET /dns/query?name=xxx&type=A` | DNS resolution test — full answer chain with TTL |
| `GET /traffic` | Total upload/download |
| `GET /memory` | In-use memory (bytes) |
| `GET /proxies` | All outbounds |
| `GET /proxies/proxy/delay?url=...&timeout=5000` | Latency test (ms) |
| `GET /rules` | All routing rules in eval order |
| `GET /logs` | Recent log entries |

### Example responses

**DNS query:**
```json
{
  "Answer": [
    {"name": "www.baidu.com.", "type": 5, "TTL": 58, "data": "www.a.shifen.com."},
    {"name": "www.a.shifen.com.", "type": 1, "TTL": 58, "data": "183.2.172.177"}
  ],
  "Status": 0
}
```

**Proxy delay:** `{"delay": 504}`

**Connection:**
```json
{
  "id": "uuid",
  "metadata": {
    "network": "tcp", "sourceIP": "172.19.0.1", "destinationIP": "142.251.45.10",
    "host": "www.google.com", "processPath": "chrome.exe"
  },
  "upload": 2310, "download": 6879,
  "start": "2026-07-22T11:16:36Z",
  "rule": "rule_set=geosite-geolocation-!cn => route(proxy)",
  "chains": ["proxy"]
}
```

### Build tag requirement

Clash API may require `-tags with_clash_api` at build time. Pre-built releases from SagerNet include it.

### Monitoring dashboard template

See `templates/monitor.html` for a ready-to-use HTML dashboard that fetches from the Clash API — shows traffic, connections, DNS tester, latency chart, and route distribution pie. Drop it next to sing-box and open in browser.

### Official Dashboard (gRPC) — NOT compatible with CLI sing-box

The [official sing-box dashboard](https://github.com/SagerNet/sing-box-dashboard) (React + gRPC-Web) uses the `StartedService` gRPC API (`daemon/server.go`). This API is ONLY available when sing-box is launched by the desktop app or libbox — CLI `sing-box.exe run` does NOT expose it.

The dashboard's connection form asks for:
- **URL**: `host:port` (no http://, no path) — gRPC-Web endpoint
- **Secret**: optional Bearer token

These will fail against a CLI sing-box. Use the Clash REST API (`/connections`, `/traffic`, etc.) instead.

### Full-stack monitoring project

For a production-grade Flutter + sing-box monitoring system (DNS timing, TCP timing, TLS timing, SQLite storage, alert rules, SSE streaming), see the design document at `~/works/sing-box-monitor/DESIGN.md`. Architecture: inject timing at 4 points (`dns/client.go`, `common/dialer`, `common/tls/client.go`, `trafficcontrol`), store in SQLite, stream via SSE, consume in Flutter.

## TUN Mode: Proxy Environment Variable Pitfall

**Critical**: When sing-box is in TUN mode (intercepting all traffic at the network level), do NOT set `http_proxy` / `https_proxy` environment variables pointing to `127.0.0.1:10809` or any other local proxy port. TUN mode replaces the need for a local SOCKS5/HTTP proxy. The env vars will cause connection failures because:

1. Tools (curl, Dart pub, git) try to connect to `127.0.0.1:PORT` directly
2. No service is listening on that port (TUN operates at the network interface level, not as a proxy)
3. All connections fail with "Connection refused" or "Failed to connect"

**Symptom**: `curl` / `flutter pub get` / `git clone` all fail even though basic internet works in browsers.
**Fix**: `unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY`

### Verify TUN routing

```bash
# Check TUN interface exists and has default route
powershell.exe -Command "Get-NetAdapter -Name 'singtun' | Select Name,Status"
powershell.exe -Command "Get-NetRoute -InterfaceAlias 'singtun' | Select DestinationPrefix,NextHop"
```

A working TUN setup shows `0.0.0.0/0 → 172.19.0.2` in the routing table.

## Server Config Notes (Linux 服务端)

```json
{
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "route": {
    "rules": [{"port": 53, "action": "hijack-dns"}],
    "final": "direct",
    "auto_detect_interface": true
  }
}
```
- `final` 必须存在，否则转发无默认出站
- `auto_detect_interface: true` 确保正确路由
- outbound 必须有 `tag`

### Reality handshake CDN selection

服务端 `handshake.server` 若选择了中国境内的 CDN（如 `v.qq.com`，从美国连 140ms）→ 每次新连接握手都跨国延迟。

**修复**：换成服务端本地的 CDN：
```bash
sed -i 's/v.qq.com/www.google.com/' /etc/sing-box/config.json
systemctl restart sing-box
```

### Windows 管理员重启

sing-box TUN 需要管理员权限。从非管理员终端重启时，`taskkill` 会被拒绝。需要用**嵌套提升**：

```powershell
powershell -Command "Start-Process powershell -ArgumentList '-Command',
  'taskkill /F /IM sing-box.exe; Start-Sleep 2;
   & \"C:\Programs\sing-box\sing-box.exe\" run -c config.json'
  -WorkingDirectory 'C:\Programs\sing-box' -Verb RunAs -WindowStyle Hidden"
```

## Verification

### 分流效果验证

国内直连 <0.3s，国外走代理 1-3s（取决于 Reality 握手延迟）：
```
Baidu    200 0.10s  ← direct
Bilibili 200 0.16s  ← direct
Google   204 1.03s  ← proxy
YouTube  200 2.96s  ← proxy
GitHub   200 1.91s  ← proxy
```
日志验证：`grep "outbound/direct"` 应只出现国内 IP，`grep "outbound/vless"` 出现国外 IP。

### 基本验证命令

```bash
sing-box.exe check -c config.json
grep "ERROR" singbox.log
grep "outbound/vless" singbox.log | grep "outbound connection to" | tail -10
grep "outbound/direct" singbox.log | grep "outbound connection to" | tail -10
nslookup doubleclick.net          # should show Query refused (ad blocking)
nslookup baidu.com                # should resolve normally
curl -s -o /dev/null -w "%{http_code} %{time_total}s\\n" https://www.google.com/generate_204
```

常见坑快速参考

| 现象 | 原因 | 修复 |
|------|------|------|
| DNS EOF 错误 | DNS 走代理被拒 | `final: dns-direct` + 路由规则 `dns → direct` |
| Google 超时 5s | geoip-cn 放 cn 规则前误判 | `geosite-geolocation-!cn → proxy` 放最前 |
| DoH bootstrap 失败 | 域名 DNS 需要先解析域名自身 | 加 `domain_resolver` 指到 IP-DNS |
| IPv6 地址导致 SSL 失败 | TUN 有 IPv6 → 浏览器优先 IPv6 → 代理不通 | 删掉 TUN IPv6 address |
| ERR_NAME_NOT_RESOLVED | strict_route=true DNS 环路 | strict_route: false |

## Build tag requirements

| Feature | Build tag |
|---------|-----------|
| VLESS Reality + uTLS | `with_utls` |
| TUN mode | `with_gvisor` (Windows) |
| WireGuard | `with_wireguard` |
| Clash API | `with_clash_api` |

Pre-built releases from [SagerNet](https://github.com/SagerNet/sing-box/releases) include all tags.

### libbox.aar / libbox.dll (Android & Windows embedding)

See `references/build-libbox-aar.md` for the full build guide — NDK setup, SagerNet gomobile fork, JDK version patching, common failures, Kotlin integration gotchas (PlatformInterface, WIFIState, LocalDNSTransport), and Windows DLL build via cgo.

### Flutter/Android integration quick reference

When embedding sing-box in a Flutter app:
- **Must use `flutter create`** — manually assembled projects missing Gradle wrapper fail `flutter build apk`
- **AGP 8.x**: remove `package` from `AndroidManifest.xml`, namespace in `build.gradle.kts`
- **local.properties** needs Windows backslash paths: `flutter.sdk=C:\\Users\\...\\.puro\\envs\\default\\flutter`
- **Riverpod 3.x**: `StateProvider` removed; use `Notifier<T>` with `build()` method; `final` fields in initializer list
- **Desktop testing**: `flutter create --platforms windows .` then `flutter run -d windows`; mock data generator avoids needing a device

