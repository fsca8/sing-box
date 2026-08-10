---
name: netbird-overlay-debugging
description: singbird/netbird 集成排障:overlay 入站桥接(BridgeTCP/expose_ports)、SSH 链路判读、连接慢/方向不对称诊断(relay vs P2P)、nb-engine.log 日志获取、ICE 状态判读。Android nbembed netstack 模式专用。
---

# Netbird Overlay 入站 & 慢/不稳定排障

## 触发场景
- SSH/服务连不上 netbird overlay IP(Android 设备)
- netbird 连接慢、不稳定、时好时坏
- 需要看 netbird 引擎日志但 logcat 里什么都没有

## 1. 架构前提(必读)
nbembed 是**进程内 netstack**(Android 上 sing-box 占用 VpnService,netbird 无内核 TUN):
- 隧道入站 TCP:netstack 自己完成 SYN-ACK(握手能成),但无监听者则数据无处投递 → SSH 显示 banner 超时
- Termux sshd 监听内核栈 0.0.0.0:8022,与 netstack 用户态栈隔离 → 数据到不了
- ping overlay 0ms + TTL=128 = **本地栈回显**(sing-box TUN 消化 ICMP),测不出真实隧道

## 2. 入站桥接方案(零改 netbird)
netbird embed 现成 API:`client.ListenTCP(":port")` 在 netstack 内建监听,对端 peer 入站能被 Accept,Accept 后 io.Copy 双向转发到 127.0.0.1:port。

实施(已落地):
- sing-box `experimental/netbird_integration/bridge.go`:BridgeTCP/StopBridge/StopAllBridges,一端口一监听(静态 1 goroutine + 1 gvisor endpoint 几 KB)
- `Config.ExposePorts`(json `expose_ports`),StartAll 引擎启动后自动桥接
- libbox 导出 `NetbirdBridgeTCP(port, target)` / `NetbirdStopBridge(port)` 给 Kotlin
- Flutter:settings_page 的 Netbird 卡片加 Expose Ports 编辑器;`_normalizeTarget` 空/纯端口自动补 `127.0.0.1:<port>`
- 配置示例:netbird-config.json 加 `"expose_ports":[{"port":8022,"target":"127.0.0.1:8022"}]`

坑:Flutter 启动会重写 netbird-config.json,内存 _nbExposePorts 为空时会覆盖手写 expose_ports → `_writeNetbirdConfigFile` 内存空时须从旧文件读回保留。

## 3. SSH 链路判读(overlay)
| 现象 | 含义 |
|------|------|
| banner 超时 | 数据无投递(netstack 无监听者) |
| `Permission denied (publickey,password,...)` | 链路通,已到 sshd 认证 |
| `Connection refused` | netstack 无监听直接 RST |
| 局域网直连通、overlay 不通 | 确认是 netstack 入站问题(对照 homesfy 内核 TUN 模式) |

## 4. 连接慢/不稳定 → 方向不对称
实测(同局域网 192.168.31.x):
- **入站方向**(他人连 mipad)走 relay ~50ms 波动大(max 117ms)
- **mipad 主动发起**走 P2P ~2.8ms 稳定
定位法:同局域网另一 netbird peer 双向 ping 对照(homesfy 192.168.31.196 vs mipad),单向 50ms / 反向 2ms = relay/P2P 不对称。隧道本身是好的(P2P 已激活),慢在 relay 中转路径。

## 5. nb-engine.log 诊断法(关键!)
nbembed logrus 默认 os.Stderr 被 gomobile 吞掉 → Android logcat/singbox.log 都看不到引擎日志。须在 Engine.Start 显式:
```go
opts.LogOutput = os.OpenFile(filepath.Join(e.cfg.DataDir, "nb-engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
```
引擎日志唯一来源,含 relay/P2P/ICE/bridge 状态。

ICE 日志判读:
- `set ICE to active` + `configure WireGuard endpoint to: 192.168.x` = P2P 激活
- `OnRemoteAnswer, priority: None, status ICE: Disconnected` = 打洞中/未成
- `start to communicate with peer via relay` = 走阿里云中转
- `wgProxy for relay connection: 127.1.x.x` = relay 代理(netbird 虚拟地址)
- `first wg handshake detected within: 0.00sec` = WG 握手成功(P2P 通)

## 6. 服务器侧佐证(47.120.70.32)
- store.db `peers` 表:peer IP/connected/来源公网 IP
- `proxies` 表 0 节点 = Expose 不可用(需 proxy 节点 + peer_expose_enabled + 组)
- `records`/`zones` 自定义 DNS:zone 按 distribution_groups 分发,server-g 拿不到 client-g 的 zone → 公网 DNS 解析失败
- setup key 离线构造:明文大写 UUID,库中存 SHA256+base64;one-off usage_limit=1,reusable 可复用
