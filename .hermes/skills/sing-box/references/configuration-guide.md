# sing-box 通用配置与调试指南

通用 sing-box 知识（从旧主 skill 拆出，v1.13+ 验证）。项目特定内容见主 SKILL.md 和其他 references。

## 配置结构（v1.13+）

```json
{
  "log": { "level": "info", "output": "singbox.log", "timestamp": true },
  "dns": { "servers": [...], "strategy": "ipv4_only" },
  "inbounds": [{ "type": "tun", ... }],
  "outbounds": [{ "type": "vless", ... }, { "type": "direct" }, { "type": "block" }],
  "route": { "rules": [...], "rule_set": [...], "final": "proxy" }
}
```

## DNS 排障

1. **strict_route:true + ERR_NAME_NOT_RESOLVED**：WFP 重定向 DNS 进 TUN → fake DNS 转发走系统栈 → 被 WFP 再捕获 → 循环。Fix：`strict_route:false` + `route_exclude_address`（223.5.5.5/32 等）+ 规则 `{"protocol":"dns","outbound":"direct"}`。
2. **ERR_CONNECTION_CLOSED + IPv6 + IPv4-only 代理**：TUN 有 IPv6 地址 → app 先试 IPv6 → 代理只支持 IPv4 → 失败。Fix：TUN `address: ["172.19.0.1/30"]`（去掉 IPv6）+ DNS `strategy: "ipv4_only"`。
3. **DNS 查询走代理 EOF**：`dns.final` 用 detour:proxy 的服务器（VLESS UDP 解析 EOF）。Fix：`dns.final: "dns-direct"`；规则顺序 **hijack-dns 必须在 dns→direct 之前**（否则 DNS 模块被绕过）。

## 性能问题

- **代理连接 5-18s**（预期 1-2s）：① TCP dial 超时 5s = 服务器丢 SYN（限速/SYN 队列满）→ 服务器侧问题；② HandshakeContext 卡住 = Reality 验证/服务器 CPU 饱和。
- **计时埋点**（monitor 架构）：RecordDNS（dns/client.go exchange）、RecordTCP（**必须放 resolveDialer 不是 DefaultDialer**——ParallelDialer 走 DialParallel 跳过 DefaultDialer hooks）、RecordTLS（common/tls/client.go）。DialMeta 经 context 从 route/route.go 注入，connID 关联 SQLite 行。

## 协议对比调试

- **换 VMess 隔离问题**：服务端加 vmess inbound（不同端口）+ 客户端换 vmess outbound。VMess 开销远小于 VLESS+Reality+uTLS——若 VMess 也慢 → 不是协议问题。
- **TUN vs SOCKS5**：换 `mixed` inbound（127.0.0.1:2080）+ curl -x socks5 测。SOCKS5 快 TUN 慢 = Windows TUN 驱动问题；都慢 = 代理服务器/网络路径。

## Rule Set Deadlock（remote + download_detour: proxy）

**症状**：首次启动 FATAL `initialize rule-set[0]: ... EOF`（规则集在 GitHub，国内被墙需代理，但代理路由需规则集 → 死锁；缓存清空后必现）。
**Fix A**（推荐）：`type: local` + 手动代理下载 .srs（**绝对路径**）。
**Fix B**：保持 remote，启动前设 `HTTPS_PROXY` 环境变量。

## TUN 模式代理环境变量坑

TUN 模式下**不要设 http_proxy/https_proxy** 指向本地代理端口（TUN 已接管全部流量，无本地端口监听 → 所有连接 refused）。`unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY`。
验证 TUN 路由：`Get-NetAdapter -Name singtun` + `Get-NetRoute -InterfaceAlias singtun`（正常：`0.0.0.0/0 → 172.19.0.2`）。

## 服务器侧诊断

`scripts/netcheck.sh`（远程服务器）：健康检查、端口监听、ping 关键目标、带宽测试、HTTP 下载实测、防火墙限速、网卡错误。

## 其他配置章节

- **GeoIP/Geosite 分流** → `references/split-routing-geoip.md`
- **DoH/DoT**、**DNS 级广告拦截**、**Clash API /monitor 端点** → 见旧版对应配置模式（GeoIP 分流文件有 DNS 服务器配置示例）
- **服务器端配置**（Linux）→ 参考 netcheck 脚本 + 分流文件

## 构建 tag 要求

```
with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_netbird(可选)
```
缺 tag 会导致对应功能编译失败/缺失（如 with_clash_api 缺 → 无 127.0.0.1:9090）。
