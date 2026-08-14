---
name: netbird-stun-tun-hijack
description: "singbird P2P 打洞失败确定性根因: Windows(netstack) STUN 探测被 sing-box TUN 劫持→srflx 候选缺失(非污染、非对称NAT); Clash API 字节不对称判据; pion fork 插桩; 编译/部署坑。"
version: 1.0.0
created_by: agent
platforms: [windows, linux]
tags: [netbird, sing-box, stun, ice, p2p, tun, debugging]
---

# netbird STUN 被 TUN 劫持 → srflx 缺失 → P2P 不可能

2026-08-13 实测闭环。适用于 singbird(sing-box+netbird 合体)两端 P2P 打洞失败、走 relay、SSH 建连 4-6s。

## 决定性对照实验(排除对称 NAT)

把 Windows 侧换成**官方 netbird 客户端**(独立进程, 无 singtun): 同家庭 NAT、同跨运营商路径
(深圳电信↔广东电信)下 **P2P 直连成功** —— `netbird status -d` 显示
`Connection type: P2P, ICE candidate srflx/srflx, 端点 113.89.16.32:51820 ↔ 27.40.78.105:38880`,
WireGuard 握手持续刷新, 隧道 TCP RTT 72-79ms(=物理直连, relay 应为 ~140ms 双程), SSH 0.7-0.95s。
→ **铁证: 根因是嵌入集成的 TUN 劫持, 不是对称 NAT**(期间公网 IP 还从 113.89.105.202 换成
113.89.16.32, 运营商重分配, 曾误导排查方向)。

## 根因(证据链, 按硬度)

Windows 内嵌引擎(netstack 模式, 无 fwmark/netlink 机制)的 STUN 探测被 sing-box TUN 劫持 →
STUN 响应回不来 → srflx 候选从未发现 → checklist 只剩"私网 host × 远端"pair → 跨 NAT 不可达 →
P2P 数学上不可能 → relay 兜底。

1. **候选干净**: 发现的 srflx 都是真实公网 IP(非代理 IP), 排除污染。
2. **Clash API 字节不对称(铁证)**: STUN 连接 `47.120.70.32:3478 udp src=172.19.0.1:51820`
   出现 `up=6644 down=40`(源=TUN 接口 IP, 请求大量发出、响应几乎为0)。
3. **STUN 超时**: 引擎日志 `Failed get server reflexive address ... timeout while waiting for XORMappedAddr`。
4. **不对称判据**: homesfy(Linux, fwmark 旁路)srflx 正常发现; Windows(netstack)srflx 缺失 → 问题在 Windows 侧。

## 判据对比(勿混淆)

| 根因 | srflx 状态 | 关键判据 |
|------|-----------|---------|
| srflx 污染(见 netbird-srflx-poisoning) | =代理IP(错误值) | `configure WireGuard endpoint to: <代理IP>` |
| **STUN 被 TUN 劫持(本场景)** | **缺失**(STUN 超时) | Clash API `3478/udp` up>>down + src=TUN IP |
| 对称 NAT | 正常发现 | 官方客户端同环境 P2P 成功 → **已排除**(勿再怀疑) |

**"Failed to ping without candidate pairs" 是红鲱鱼**: 候选到达前 checklist 短暂为空, 不能据此断定
"无 pair"。fork 加 addPair 日志实测 pair 会形成(checklist 涨到 12), 真正失败在连通性(私网 pair 不可达)。

## 治本修复(嵌入集成) — ✅ 已实现(2026-08-14, 方案A落地)

`process_path/ip_cidr → direct` 规则**不够**: 只改变 sing-box 出站(direct/proxy), 流量已先进 TUN
(源变 172.19.0.1), 响应回程仍坏。正解是让引擎控制面 socket **根本不进 TUN**:

**已实现**: netbird 嵌入旁路开关 `netstack.SetEmbedded/IsEmbedded`(client/iface/netstack/embedded.go),
4 处判定(CustomRoutingDisabled/Windows advancedRouting/Linux advancedRouting/Android ControlProtectSocket)
加 `&& !netstack.IsEmbedded()` — netbird 的旁路机制本来就有(Windows `IP_UNICAST_IF`(net_windows.go
applyUnicastIFToSocket)/Linux fwmark 0x1BD00→main/Android protect), 只是被 `netstack.IsEnabled()`
一刀切禁用; nbembed.New userspace 分支自动 SetEmbedded(true); Windows 集成层
`nbnet.SetVPNInterfaceName("singtun")` 让 GetBestInterface 排除 singtun 选物理网卡;
Android 链路: Kotlin VpnService→`Libbox.setProtector`(Protector 接口)→
`netbird_integration.SetAndroidProtectFn`→`nbnet.SetAndroidProtectSocketFn`。
`process_path` 旁路规则已移除(死代码); 保留 ip_cidr ctlIPs→direct + domain_suffix(管理面 TCP 仍走
sing-box 路由层) + dns-direct。实测 Windows: srflx 无手动路由自动发现 + ICE 1s Connected(原 12s
Failed) + SSH<1s + 隧道 RTT 56-64ms。

- 关键 commit: netbird a57420b76(开关)/51e7e7aff(nbembed), sing-box baa5907c6(SetVPNInterfaceName)/
  3381b1fb9(移除process_path)/c90d57670(Android protect), singbird 06aff5e(VpnService Protector)。
- 注意: Android libbox.aar 需重新生成(含 Protector 接口导出); WG 与 ICE 共享 UDPMux socket,
  一个旁路修复点覆盖控制面+数据面。
- 临时救急(旧): `New-NetRoute -DestinationPrefix <ctlIP>/32 -NextHop <物理网关> -InterfaceAlias WLAN`
  (只对已知 IP 有效; 对端动态公网 IP 覆盖不到, 且重启失效)。

## pion fork 插桩(看 ICE 内部)

netbird 用 fork: go.mod `replace github.com/pion/ice/v4 => github.com/netbirdio/ice/v4`(fork 保留原 module path)。
加日志: cp module cache 的 fork 到本地(缓存文件只读, 先 `chmod -R u+w`), 在 agent.go 的
addPair/addCandidate/addRemoteCandidate 加日志, go.work 加 `replace github.com/pion/ice/v4 => ../本地fork`
(go.work replace 覆盖 go.mod replace), 用完撤掉。

## 编译/部署坑

- `installControlPlaneMarkRule`(Linux fwmark 修复 68d02fa85)无非 Linux stub → Windows/Android 编译
  `undefined: installControlPlaneMarkRule`。补 engine_rules_other.go(//go:build !linux || android)空 stub。
- Windows 部署 singbird(UAC requireAdministrator): 普通 shell 杀不掉进程, 须 `Start-Process -Verb RunAs`
  提权(弹 UAC 用户批准)。
- 部署 .ps1 必须纯 ASCII: PowerShell 5.1 按 GBK 读无 BOM 的 UTF-8, 中文注释会解析失败。

关联: Linux 侧根因/修复见 netbird-hole-punching(sing-box 仓库); 污染变体见 netbird-srflx-poisoning。
