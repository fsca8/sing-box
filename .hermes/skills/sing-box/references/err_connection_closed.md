# ERR_CONNECTION_CLOSED 诊断记录

## 2026-07-21 实际案例

### 环境
- sing-box 1.13.14 Windows 客户端
- VLESS+Reality+uTLS (TCP only)
- 代理服务器：154.51.40.245:12399 (纯 IPv4)
- TUN 模式，`stack: mixed`

### 症状
- 浏览器报 `ERR_CONNECTION_CLOSED`
- `nslookup` 能解析域名（返回 IPv6 + IPv4 混合）
- `curl` 无 `-4` 标志：`HTTP=000, exit code 35` (SSL 错误)
- `curl -4`：全部正常

### 日志特征

**错误 1 — DNS UDP 走代理 EOF**（原配置 `strict_route: true` + `dns final: dns-remote`）：
```
ERROR listen packet connection using outbound/vless[proxy]: EOF
```
DNS 查询全部走 VLESS proxy（UDP），服务端不支持 → EOF。

**错误 2 — 连接被远程强制关闭**（`strict_route: true` 环路导致长连接失效）：
```
ERROR connection upload closed: raw-read tcp4 172.19.0.1:52757->172.19.0.2:10018: An existing connection was forcibly closed by the remote host.
```

**错误 3 — IPv6 目标连接超时**（TUN 有 IPv6 地址，DNS 返回 AAAA 记录优先）：
```
ERROR open connection to [240e:e9:6003:211::f0]:443 using outbound/vless[proxy]: dial tcp 154.51.40.245:12399: i/o timeout
```

### 修复后的正常日志
TCP 连接正常通过代理，约 330ms 握手：
```
INFO outbound/vless[proxy]: outbound connection to 142.250.31.94:443
INFO outbound/vless[proxy]: outbound connection to 142.250.31.94:443  (330ms)
```
DNS 不再出现 ERROR。

### 最终修复清单
1. `strict_route: false` → 解除 DNS 环路
2. `route_exclude_address: ["223.5.5.5/32", "1.12.12.12/32"]` → DNS 服务器绕过 TUN
3. `dns.final: dns-direct` → DNS 直连 AliDNS，不走代理
4. `dns.strategy: ipv4_only` → DNS 仅返回 IPv4（保险）
5. 移除 TUN IPv6 address → 防止应用走 IPv6 栈
6. Route rules：`hijack-dns` 在前，`dns → direct` 在后
