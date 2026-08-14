---
name: netbird-ice-punch-diagnostics
description: sing-box + netbird 双引擎 P2P 打洞失败诊断与修复 — ip rule 劫持根因、回程包接口判据、A/B 测试、dev.sh GOOS 坑
---

# ICE 打洞失败诊断（sing-box + netbird 双引擎共存）

2026-08-12 深度排查总结。适用：homesfy/Windows 两端 P2P 打洞失败、走 relay/TURN 的场景。

## 症状分级（先确认数据面走哪条路）

| 路径 | RTT 量级 | 判定依据 |
|------|---------|---------|
| P2P 直连 | ~19ms | Windows nb-engine.log `configure WireGuard endpoint to: <对端真实公网IP>` + Dump stat `P2PConnected > 0` |
| netbird relay (ws) | 腿程级 ~38ms | `status relay: Connected`，v0.76.3 + systemd 直部署后 relay 开销≈0 |
| TURN 中继 | ~340ms | 引擎日志 turn conn；8.12 的 350ms 同源 |

## 根因链（本次实锤）

1. **sing-box auto_route 劫持 netbird 出站源 IP**：ip rule `9000: from all to 172.19.0.0/30 lookup 2022`（TUN 网段→表 2022→singtun）在 local 表（32767）之前——**目标 = TUN 接口 IP（172.19.0.1）的入站被导向 singtun 而非本地投递** → wg socket 收不到回程。
   - 背景：netbird 打洞 socket 绑 0.0.0.0，出站源 IP 由路由决定 → 被 auto_route 送进 singtun → 源=172.19.0.1 → 光猫 NAT 映射指向 singtun → 回程撞上 9000 规则 → 劫持。
   - **修复**：`sudo ip rule add from all to 172.19.0.1 lookup local pref 8999`（在 sing-box 规则之前精确本地投递）。注意 sing-box 重启可能重装规则段，需验证持久性。
2. **失败闭环**：握手收不到 → ICE 失败 → 打洞探测降频 → UDP NAT 映射过期（30-120s）→ 后续包被光猫直接丢弃（tcpdump 从"抓到"变"抓不到"是映射过期信号）。

## 诊断命令序列

```bash
# 1. 双端 srflx 是否干净（info 级不打，须 debug）
#    netbird-config.json 改 "log_level": "debug" + 重启引擎
grep -iE "stun|srflx" nb-engine.log | tail
#    期望：discovered local candidate udp4 srflx <真实公网IP:port>

# 2. 回程包进哪个接口（关键判据）
sudo tcpdump -i any -n 'udp and port <wg端口>' -c 8
#    singtun In = 被 TUN 劫持；lo In = 本地投递正常

# 3. ip rule 顺序（找劫持规则）
ip rule show
#    Linux 默认应有 pref 0/低优先级的 local 规则；sing-box auto_route 可能删掉/抢占

# 4. 本地地址投递验证
ip route show table local | grep 172.19   # local 172.19.0.1 dev singtun ...（记录在，但被 rule 顺序跳过）

# 5. 引擎状态（10 分钟周期 dump）
grep "Dump stat" nb-engine.log | grep <peer>
#    P2PConnected / RemoteCandidate / SwitchToRelay 是核心指标
```

## 已排除的变量（避免重复排查）

- **客户端版本**：A/B 测试（换回 8.11 旧二进制）仍失败 → 非 client 版本问题
- **TUN 配置**：config.json 备份对比（auto_route/address 相同）
- **网络层**：tcpdump 抓到 Windows 包到达 homesfy（光猫允许入站）
- **force relay**：只由 NB_FORCE_RELAY 环境变量控制，无服务端下发

## 重要坑

1. **dev.sh 默认 GOOS=windows**（`GOOS="${GOOS:-windows}"`）——homesfy 编 Linux 目标必须 `GOOS=linux ./dev.sh netbird release`，否则产出 PE 导致 systemd `Exec format error (203/EXEC)`。
2. **运行中二进制替换**：cp 覆盖报 `Text file busy` → cp 到 .new + `mv -f` 原子替换。
3. **systemd 限流**：连续失败后 `Start request repeated too quickly` → `sudo systemctl reset-failed <unit>` 再 start。
4. **服务端回滚测试教训**：v0.76.3 客户端与 0.69 服务端不兼容（relay not supported、dump stat 停止）——回滚 A/B 需双端同版本才干净。
5. **"skipping remote answer message because receiver not ready"** = handshaker 通道积压背压丢弃（handshaker.go select default），不是启动时序；协商消息量正常很小（9 轮 offer/answer ≈ 18 条），勿误判为风暴。
6. **OnNewOffer session ID 变化自动重建 ICE agent**（worker_ice.go）——对端重启后不需要手动重启本端，ICE 会自主重试（3 次快速 + hourly）。
7. **rule-sets 未就绪窗口污染 srflx**：引擎首轮 STUN 探测可能早于远程 geoip/geosite 加载 → 探测落 final→proxy → srflx=代理出口。已修：注入 `ip_cidr <mgmt host IP>/32 → direct`（不依赖规则集）。
