# GeoIP/Geosite Split Routing — Full Working Config

Complete sing-box DNS + route section for China/non-China split routing with DoH encryption.

## DNS Section

```json
"dns": {
    "strategy": "ipv4_only",
    "servers": [
      {
        "type": "https",
        "tag": "dns-direct",
        "server": "dns.alidns.com",
        "server_port": 443,
        "path": "/dns-query",
        "domain_resolver": "dns-remote"
      },
      {
        "type": "https",
        "tag": "dns-remote",
        "server": "1.1.1.1",
        "server_port": 443,
        "path": "/dns-query",
        "detour": "proxy"
      }
    ],
    "rules": [
      { "rule_set": "geosite-category-ads", "action": "reject" },
      { "rule_set": "geosite-cn", "server": "dns-direct" }
    ],
    "final": "dns-remote"
  }
```

## Route Section

```json
"route": {
    "auto_detect_interface": true,
    "default_domain_resolver": "dns-direct",

    "rule_set": [
      {
        "type": "remote",
        "tag": "geoip-cn",
        "format": "binary",
        "url": "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs",
        "download_detour": "proxy"
      },
      {
        "type": "remote",
        "tag": "geosite-cn",
        "format": "binary",
        "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
        "download_detour": "proxy"
      },
      {
        "type": "remote",
        "tag": "geosite-category-ads",
        "format": "binary",
        "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads.srs",
        "download_detour": "proxy"
      },
      {
        "type": "remote",
        "tag": "geosite-geolocation-!cn",
        "format": "binary",
        "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs",
        "download_detour": "proxy"
      }
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

## Rule Set Type: remote vs local

**`type: remote` with `download_detour: proxy` has a chicken-and-egg deadlock** when the local cache is empty: rule sets are on GitHub (blocked), need proxy to download, but proxy routing needs rule sets loaded. Solution:

**Option A — `type: local` (recommended for stability):**
```bash
# Download .srs files once via system proxy
export https_proxy=http://127.0.0.1:10809
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads.srs
curl -sLO https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs
```
```json
{
  "type": "local",
  "tag": "geoip-cn",
  "format": "binary",
  "path": "C:\\Programs\\sing-box\\geoip-cn.srs"
}
```
Use **absolute paths** — sing-box may run from a different working directory.

**Option B — `type: remote` (convenience, needs initial cache):**
Works once cache is populated. Fails on first start if cache is empty. Set `HTTP_PROXY`/`HTTPS_PROXY` env vars for bootstrap downloads.

## Rule Ordering Rationale (top-down, first-match-wins)

| # | Rule | Purpose |
|---|------|---------|
| 1 | `sniff` | Extract TLS SNI domain for domain-based routing |
| 2 | `hijack-dns` | Intercept DNS → sing-box DNS module (uses ipv4_only strategy) |
| 3 | `geosite-geolocation-!cn → proxy` | Foreign domains go through proxy — MUST come before geoip-cn |
| 4 | `geosite-cn → direct` | Chinese domains go direct (Baidu, Bilibili, etc.) |
| 5 | `geoip-cn → direct` | Chinese IPs go direct (fallback) |
| 6 | `protocol: dns → direct` | Safety net — stray DNS packets go direct |
| final | `proxy` | Everything else through proxy |

## Critical Pitfalls

### 1. SagerNet geoip-cn misclassifies Google IPs

**Observed 2026-07:** `geoip-cn.srs` includes Google IP ranges (74.125.x.x, 108.177.x.x, 172.217.x.x). If `geoip-cn → direct` is placed BEFORE `geosite-geolocation-!cn → proxy`, Google connections go direct and timeout after 5s.

**Symptom:**
```
ERROR open connection to 172.217.208.94:443 using outbound/direct[direct]: dial tcp ... i/o timeout
```

**Fix:** Rule #3 (`geosite-geolocation-!cn → proxy`) MUST precede rule #5 (`geoip-cn → direct`).

### 2. hijack-dns MUST be before dns → direct

If reversed, all DNS bypasses sing-box's DNS module. The `strategy: ipv4_only` setting has no effect, and DNS returns IPv6 addresses that fail through IPv4-only proxy.

### 3. Domain-based DoH needs domain_resolver bootstrap

Sing-box must resolve the DoH server's hostname first. `dns.alidns.com` requires `"domain_resolver": "dns-remote"` to bootstrap via 1.1.1.1 (raw IP). Without it:
```
FATAL initialize DNS server[0]: missing domain resolver for domain server address
```

DoH uses TCP/443 — `route_exclude_address` in TUN is NOT needed (only for UDP/53 DNS).

## Verification

```bash
# Config validation
sing-box.exe check -c config.json

# Quick test
curl -s -o /dev/null -w "%{http_code} %{time_total}s\n" --connect-timeout 10 https://www.baidu.com
curl -s -o /dev/null -w "%{http_code} %{time_total}s\n" --connect-timeout 10 https://www.google.com/generate_204

# Routing audit
grep "outbound/direct.*outbound connection to" singbox.log | tail -10
grep "outbound/vless.*outbound connection to" singbox.log | tail -10
```

## Ad Blocking via geosite-category-ads

DNS-level ad blocking built into sing-box — no separate software needed.

### Config (DNS rule only)

```json
"dns": {
    "rules": [
      { "rule_set": "geosite-category-ads", "action": "reject" },
      ...
    ]
}
```

`action: "reject"` returns "Query refused" for ad domains — applications get no IP, ads don't load.

### Verification

```bash
# Ad domains — should show "Query refused"
nslookup doubleclick.net
nslookup googleadservices.com
nslookup pagead2.googlesyndication.com

# Normal domains — should resolve normally
nslookup baidu.com
nslookup github.com
```

Expected output for blocked domains:
```
*** UnKnown can't find doubleclick.net: Query refused
```

### Rule set source

```
geosite-category-ads.srs
  └── from SagerNet sing-geosite (same repo as geosite-cn)
  └── auto-updates with other rule sets
  └── download_detour: proxy (GitHub blocked in China)
```

## Timing Benchmarks (Los Angeles ColoCrossing, ~210ms RTT)

| Site | Route | Expected |
|------|-------|----------|
| Baidu | direct | <0.3s |
| Bilibili | direct | <0.3s |
| Google | proxy | 0.9-1.5s |
| YouTube | proxy | 2-3s |
| GitHub | proxy | 1.5-2.5s |
| Cloudflare | proxy | 2-4s |
