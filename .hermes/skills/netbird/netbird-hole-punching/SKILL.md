---
name: netbird-hole-punching
description: sing-box TUN 与 netbird 内嵌共存时 P2P 打洞失败的根因链诊断与修复（DisableClientRoutes→fwmark 规则缺失→控制面被 TUN 劫持）；含 srflx 污染、dev.sh GOOS 坑等关联知识
---

# netbird P2P 打洞失败诊断（TUN 共存场景）

2026-08-12 实测验证的完整根因链。现象：双端 srflx 候选干净（debug 日志确认）、
网络层 UDP 通（tcpdump 抓到对端打洞包到达）、但 P2PConnected 恒为 0，数据走 relay。

## 根因链（已闭环）

1. 集成层启动 netbird 时 `opts.DisableClientRoutes = true`（避免与 sing-box TUN 抢默认路由）
2. `DisableClientRoutes` → routemanager 的 SetupRouting 被跳过 → 本该安装的
   `fwmark 0x1BD00 (ControlPlaneMark) → NetbirdVPNTable` 路由规则（systemops_linux.go
   预定义的 priority 110 规则）**没装上**
3. 打洞/ICE socket（sharedsock raw socket，kernel 模式）**有** ControlPlaneMark
   （AdvancedRouting 开时 SetSocketMark 生效），但**没有 fwmark 路由规则响应** → mark 无效
4. 打洞包走默认路由 → 被 sing-box auto_route 的 `0.0.0.0/1 → singtun`
   （ip rule 9003/2022 表）劫持 → 内核选源 IP = **singtun 接口地址（如 172.19.0.1）**
5. 光猫 NAPT 学习映射：公网 port ↔ 172.19.0.1:port（TUN 内部地址）
6. 对端回程包 → NAT 转给 172.19.0.1 → 被 sing-box TUN 消费（172.19.0.1 是 TUN 接口自己的地址，
   local 表也指向 singtun——本地投递 = 给 sing-box）→ wg socket（绑 0.0.0.0）收不到 → 打洞失败
7. 打洞失败 → ICE 探测降频 → NAT 映射超时（UDP 30-120s）→ 连包都进不来

## 修复（netbird my_custom 分支，commit 68d02fa85）

新增 `client/internal/engine_rules_linux.go` + `engine.go`（wgInterface.Up 后调用）：

- 仅 `DisableClientRoutes` 场景（内嵌 sing-box 时）：安装 `fwmark 0x1BD00 → main 表` 规则
  （priority 110，V4+V6，`netlink.RuleAdd`）→ 控制面流量直出物理接口，源 IP = 物理 IP，
  NAT 映射正确，回程可达
- 关键常量：`nbnet.ControlPlaneMark = 0x1BD00`（client/net/net.go）
- 表常量用 `unix.RT_TABLE_MAIN`（vishvananda/netlink 无 RT_TABLE_MAIN，用 x/sys/unix）
- routemanager 正常模式（非 DisableClientRoutes）不干预（systemops 自己装 110 → NetbirdVPNTable）

## 诊断方法（按顺序）

1. **AdvancedRouting 状态**：引擎日志 `system supports advanced routing`。禁用条件
   （client/net/env_linux.go）：`NB_USE_NETSTACK_MODE=true`（netstack，环境变量判断）、
   `NB_DISABLE_CUSTOM_ROUTING`、legacyRouting、fwmark/规则操作不支持。
2. **查 ip rule**：`ip rule show | grep fwmark`。netbird 计划装
   `110: from all fwmark 0x1bd00 lookup 0x1bd0`。没有 = routemanager 没跑。
3. **tcpdump 看回程接口**（决定性）：homesfy `sudo tcpdump -i any -n udp port <wg端口>`，
   对端发 UDP 到公网 IP:端口。看包进 `singtun In`（被 TUN 劫持）还是 `lo In`（本地投递正常）。
   注意 NAT 映射超时：本地不打探测包时映射过期，tcpdump 抓不到也是打洞已停的信号。
4. **debug 日志看 srflx**：netbird-config.json `log_level` 改 debug + 重启，nb-engine.log 有
   `discovered local candidate udp4 srflx <公网IP>:<port>`。双端 srflx 都正确仍打洞失败
   → 问题在路径劫持（本根因），不是候选污染。
5. **A/B 测试排除客户端版本**：备份旧二进制（`cp sing-netbird sing-netbird.bak-*`）换回重启。
6. **服务端回滚测试注意**：docker 0.69 镜像 + netbird_data 命名卷 + config.yaml.bak 可回滚，但
   **v0.76.3 客户端与 0.69 服务端不兼容**（dump stat 停止、relay not supported）——回滚环境不干净，
   结论不可靠。服务端升级不是本问题原因。

## 关联坑（同轮发现）

- **dev.sh 默认 GOOS=windows**（143 行 `GOOS="${GOOS:-windows}"`）——Linux 编译必须显式
  `GOOS=linux ./dev.sh netbird release`，否则产物是 Windows PE → systemd Exec format error (203/EXEC)。
- **引擎重启协商风暴**：对端 handshaker 通道（remoteAnswerCh 小缓冲）积压丢消息
  （`skipping remote answer message because receiver not ready`）——真实协商量小（9 轮 offer/answer），
  影响有限。对端 OnNewOffer 检测 session ID 变化会**自动重建 ICE agent**（不需要对端重启）。
- **srflx 污染时序窗口**（另一根因，已修复）：StartAll 在 sing-box 创建前跑，引擎首轮 STUN 探测时
  geoip/geosite 远程规则集未加载 → 探测落 final→proxy → srflx=代理出口 → 候选污染 → P2P 半通。
  修复：InjectNetbirdJSON 注入控制面 IP 直连（解析 mgmtURL host → ip_cidr → direct，不依赖 geoip）。
  症状特征：对端 configure endpoint 出现代理服务器 IP。
- **手动 ip rule/iptables 修复无效**：`to <TUN IP> lookup local`（local 表也指向 singtun，无解）；
  SNAT 改源治标。sing-box 重启会重装 auto_route 规则段（9000-9010），手动规则在冲突优先级被覆盖。
  正解在代码层（netbird fwmark 规则）。
- **nb-engine.log 可能 root 属主**（sfy 读不了，sudo 才可读）；集成层日志在引擎 Start 后重定向到该文件。
