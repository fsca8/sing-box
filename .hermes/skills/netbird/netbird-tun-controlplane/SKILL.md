---
name: netbird-tun-controlplane
description: netbird 引擎与 sing-box TUN 共存的打洞(P2P)失败诊断与修复 — 控制面流量被 TUN 劫持(源IP变TUN接口地址→NAT回程被消费→半通)的根因链、fwmark 修复(netbird my_custom 分支)、tcpdump 验证法与全部踩坑
---

# netbird × sing-box TUN 共存：打洞失败诊断与修复

## 触发场景
netbird(kernel_tun 模式)嵌入 sing-box 等 TUN 代理, 两端 srflx 干净、网络层通、ICE 协商正常, 但 P2PConnected 恒 0, 数据走 relay/TURN。

## 根因链(2026-08-12 实锤)
1. sing-box auto_route 的 ip rule 接管默认路由:
   - `9000: from all to <tun网段> lookup 2022`
   - `9003: from 0.0.0.0 iif lo lookup 2022`
   - 注意: 标准 `pref 0 lookup local` 被 sing-box 规则挤掉
2. netbird 控制面 socket(绑 0.0.0.0)出站被劫持 → 内核选源 IP = singtun 接口 IP(如 172.19.0.1) → 光猫 NAT 映射指向 TUN 内部地址
3. 对端回程打到该映射 → 进 singtun 被 sing-box 消费(172.19.0.1 是 singtun 接口地址, `local 172.19.0.1 dev singtun` 也指向它) → wg socket 收不到 → 握手失败
4. 打洞失败 → 探测降频 → NAT 映射超时(30-120s) → 对端包直接进不来

## 修复(netbird my_custom 分支, commit 68d02fa85)
- `client/internal/engine_rules_linux.go`(`//go:build linux && !android`): installControlPlaneMarkRule()
  - netlink RuleAdd: `fwmark 0x1BD00(ControlPlaneMark) → unix.RT_TABLE_MAIN`, priority 110, FAMILY_V4+V6, `unix.EEXIST` 幂等
  - 常量: ControlPlaneMark=0x1BD00 在 `client/net/net.go`; 路由表常量用 `unix.RT_TABLE_MAIN`(vishvananda/netlink 无此常量)
- `client/internal/engine.go`: wgInterface.Up() 成功之后, `if e.config.DisableClientRoutes { installControlPlaneMarkRule() }`
  - 原因: DisableClientRoutes(sing-box 集成必开)时 routemanager SetupRouting 被跳过, 正常装 110 规则的路径消失, mark 无规则响应
- 效果: 打洞包直出物理接口(源=物理 IP) → NAT 映射指向物理 → 回程本地投递到 wg

## 验证(按序)
1. `ip rule show | grep 1bd00` — 110 规则在(引擎启动自动装, sing-box 重启不清 pref 110)
2. `sudo tcpdump -i any -n 'udp and not port 53'` — 期待 `wlp7s0 Out IP 192.168.31.196.x > 对端`(物理接口+物理源), 不是 `singtun Out 172.19.0.1`
3. 对端日志 "first wg handshake detected" — 单向通(半通: 己→对端成, 对端→己回程断)
4. P2PConnected 计数 + `configure WireGuard endpoint` 更新为真实公网 — 双向通

## 踩坑
- `ip rule add to <tunIP> lookup local pref 8999` **无效**: local 表 `local 172.19.0.1 dev singtun` 指向 singtun 接口, "本地投递"=给 sing-box
- iptables SNAT(源 172.19.0.1→物理) 有效但脆弱; 用户要求代码层 → netlink rule
- 服务端回滚 0.69 A/B 不可行: v0.76.3 client × 0.69 server 不兼容(dump 停/relay not supported), 回滚环境不干净
- A/B 换旧二进制(8.11)排除 client 版本 — 分层排除法: client 版本→配置→TUN→网络层→服务端
- Windows 引擎长时间运行会卡(dump stat 停、对端探测零响应) — 协商风暴/状态积累, 重启恢复; 判断"引擎活着"看 relay 数据面(通)而非 dump(可能停)
- Windows 侧 log_level 改 debug 会被 Flutter 重启重写回 info(singbox_controller.setNetbirdConfig) — 需改代码或重启后立即抓日志
- `configure WireGuard endpoint` 只在 ICE 打洞成功时更新; 失败保持旧值(重启后从 state.json 恢复, 含历史污染值) — 污染值残留≠"坚持用", 是"没有新 pair 可配置"
- tcpdump 抓不到对端包 ≠ 网络断: 可能 NAT 映射过期(打洞探测降频后 30-120s) — 需协商窗口内抓包或触发协商
- 同网段 peer(平板) P2P 正常(内网直连) 是很好的对照: 排除引擎本身问题, 聚焦跨网打洞路径
