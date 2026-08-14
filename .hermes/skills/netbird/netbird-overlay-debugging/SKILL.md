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

## 6.5 候选污染:引擎流量被代理规则送走 → "假 P2P 实中继"(2026-08-11 实测修复)
症状:SSH 建连 4~5s、隧道内 ping RTT ~350ms,和 frp 中转"差不多"(实测更慢 5 倍)。
根因链:netbird 引擎的 UDP(51820/STUN/ICE 探测)进自己 sing-box TUN → route 规则 `geosite-!cn → proxy` 把 STUN 探测送进 vless → srflx 候选 = **代理出口 IP**(vmrack <PROXY_VPS>)而非真实公网 IP → ICE 端点错误打洞必失败 → 回落 TURN 中继(acloud:3478),中继路径 RTT 350ms(腿程才 43ms)。
判读:
- 引擎日志 `configure WireGuard endpoint to: <代理出口IP>` = 候选污染(正确应为对端真实公网 IP)
- 灌流量看 Clash API 字节计数:涨在 `<ACLOUD>:3478`(TURN 中继)而非对端公网 IP = 数据走中继
- **netbird 客户端内置默认 STUN=stun.netbird.io:3478,服务端 config.yaml 不配 turnConfig/stuns 就永远用它**(自建服务端也一样,不是"没用 netbird.io"就豁免)
修复(两端):
- route.rules 在 `geosite-geolocation-!cn→proxy` **之前**加引擎直连:Linux 用 `process_path→direct`(独立进程 sing-netbird);Windows 引擎嵌 sing-box.exe 用域名规则 `netbird.io/<MGMT_DOMAIN>→direct`
- **域名规则有漏洞**:引擎用自带 DNS 解析出 IP 后直接按 IP 建连(无域名上下文),`domain_suffix` 规则匹配不到 → 又掉进 proxy → vmrack 候选死灰复燃。**Windows 必须也上 `process_path: [<SINGBIRD_APP_DIR>\sing-box.exe] → direct`**(只命中引擎的 TUN 自环 socket;sing-box 自己的 vless/DNS 拨号旁路 TUN 不受影响,普通 app 流量源进程不同也不受影响——process 规则匹配源进程,不破坏分流)
- dns.rules 加 `<MGMT_DOMAIN>/netbird.io→dns-direct`(默认 final=dns-remote 过代理,控制面 DNS 200~430ms→58ms)
- **peer 公网 IP 变动(运营商重拨/CGNAT 换 IP)后**:P2P 重新协商期间若残留污染候选,ICE 会选 vmrack(半通:单方向可达)→ SSH 秒级退化。判据:homesfy 日志 endpoint 又变 <PROXY_VPS> + 握手 10s+;两端 process 规则齐了之后自然恢复直连
-验证:端点变真实公网 IP(<HOME_SRV>/<CLIENT_WAN>)、隧道 RTT 350→44ms、SSH 4.5s→0.7s。
-备注:被污染的候选常混入正常候选(真实 IP 也出现),端点地址在两者间轮换是污染特征;ddvps 之类 VPS peer 的后台保活 flap 与数据路径无关,勿混淆。
- **自动注入已上线(sing-box 7a18ad7ff,2026-08-12)**:集成层启动时自动注入 `process_path[os.Executable()]→direct` + `netbird.io/<MGMT_DOMAIN>→direct` + 控制面 DNS 直连,幂等去重且兼容手写规则,kernel-TUN 模式同样注入(控制面仍走 sing-box TUN)。重装/重启不丢,新部署无需再手改 route.rules/dns.rules;旧版仍按上文手改。
- **Android 用 package_name 而非 process_path(sing-box 6a8d4b760,2026-08-12)**:Android 进程 searcher(searcher_android.go)只解析 socket UID→包名,**无进程路径**——process_path 规则在 Android 永不匹配。修复:Config 加 `package_name` 字段(netbird-config.json,Flutter Android 侧写入 app 包名),集成层注入 `{package_name:[app包名]→direct}` 替代 process_path(app 内所有引擎 socket owner=app UID,一条规则全覆盖)。链路已确认完整:route/network.go:179 tun.NewPackageManager→router.go:180→searcher_android(netlink inet_diag)→包名,不依赖外部注入

## 6. 服务器侧佐证(<ACLOUD>)
- **服务端重启后 STUN/中继全挂 = GeoLite2 下载卡死启动**:启动时 autoUpdate 会先访问 `pkgs.netbird.io`(又一处 netbird.io 依赖!)查最新版本号再下载 mmdb,从中国网络 http=000 超时 → 整个服务卡在 init,STUN socket 建了但不应答(响应卡 tx_queue)。修复:config.yaml 加 `disableGeoliteUpdate: true`(直接用本地 GeoLite2-City_*.mmdb,不联网);`disableAnonymousMetrics: true` 关掉 ingest.netbird.io 遥测。判据:日志出现 "Relay WebSocket handler added" + "STUN server listening on [::]:3478" + UDP 3478 binding 有响应 = 启动完成
|- **外部 `stuns:` 与本地 STUN 监听已解耦(v0.76.3+my_custom_server 分支,2026-08-12 上线)**:上游缺陷是 `applyRelayDefaults` 的 `!hasExternalStuns &&` 条件——配外部 stuns 就禁用本地 3478 监听;现本地监听仅由 stunPorts 驱动,外部 stuns 可并存(客户端同时下发两者,URI 去重)。旧版(0.69 镜像)仍受此缺陷约束,配外部 stuns 前先确认版本
- store.db `peers` 表:peer IP/connected/来源公网 IP
- `proxies` 表 0 节点 = Expose 不可用(需 proxy 节点 + peer_expose_enabled + 组)
- `records`/`zones` 自定义 DNS:zone 按 distribution_groups 分发,server-g 拿不到 client-g 的 zone → 公网 DNS 解析失败
- setup key 离线构造:明文大写 UUID,库中存 SHA256+base64;one-off usage_limit=1,reusable 可复用

### 服务端部署形态(2026-08-12 起,docker 容器已停)
- **systemd 直部署**:`/usr/local/bin/netbird-server -c ~/servers/netbird/config.yaml`(unit: netbird-server.service,User=ecs-user,Restart=always)
- config.yaml 三处与容器时代不同:`listenAddress: :8081`(Caddy 反代 127.0.0.1:8081 不变)、`trustedHTTPProxies: [127.0.0.1]`(原 docker 网关)、`dataDir: ~/servers/netbird/data`(docker volume 数据已复制出来;store.db/events.db/idp.db/geonames/mmdb)
- 分支 `my_custom_server`(netbird 仓库,基于 v0.76.3):stuns 解耦补丁 + version 子命令;版本查询 `netbird-server version`(输出 Upstream tag/Commit/Built/BuiltBy;上游 combined 无 version 命令是设计,此为本分支新增)
- **分支基准标注(仿 sing-box)**:netbird 仓库根 `UPSTREAM_TAG` 文件记录 my_custom / my_custom_server 的共同上游基准 tag(现 `v0.76.3`);合并官方新 tag 流程 `git fetch origin && git merge <tag>`,冲突策略与 sing-box 一致(本地引擎/集成改动保留 ours,上游重构 theirs),**合并后更新 UPSTREAM_TAG**
- 编译链(homesfy):go 1.23.2 + GOTOOLCHAIN=auto 自动拉 1.25.12 + GOPROXY=goproxy.cn + gcc 11.4 真 cgo sqlite。**Windows 本机不可编**:无 gcc,CGO_ENABLED=0 时 mattn/go-sqlite3 链接 stub 假编译(运行时 requires cgo)
  ldflags:`-X github.com/netbirdio/netbird/version.version=<tag> -X github.com/netbirdio/netbird/combined/cmd.commit=<hash> -X github.com/netbirdio/netbird/combined/cmd.date=<RFC3339> -X github.com/netbirdio/netbird/combined/cmd.builtBy=<name>`
- **回滚**:`systemctl stop netbird-server && cd ~/servers/netbird && docker compose up -d`(0.69 官方镜像 + docker volume 数据原样,秒回)
- 迁移预演法(先于正式切换):停容器→`docker cp netbird-server:/var/lib/netbird`→tar→homesfy 解压+改端口试启动→日志确认 migration + `single account mode enabled, accounts number 1`→通过才切
