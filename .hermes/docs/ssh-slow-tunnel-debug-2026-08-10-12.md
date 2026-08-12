# Singbird/Netbird Overlay SSH 慢速问题排查与修复全记录

**时间跨度**:2026-08-10 晚 ~ 2026-08-12 上午
**结论**:SSH 建连 4.5s → 0.6s;隧道 RTT 350ms → 19ms(直连 P2P)

> 本文为脱敏版,所有真实 IP/域名/用户名/仓库地址以占位符代替,真实值见本地配置文件与项目记忆。

## 占位符对照(仅本文使用)

| 占位符 | 含义 |
|--------|------|
| `<ACLOUD>` | 阿里云中继/管理机(公网 IP,跑 netbird-server docker + Caddy + frp) |
| `<PROXY_VPS>` | vless+Reality 代理出口 VPS |
| `<PEER_VPS>` | 另一台 VPS peer(后台保活,与本次问题无关) |
| `<HOME_SRV>` | homesfy 公网 IP(惠州联通,家庭 NAT) |
| `<CLIENT_WAN>` | Windows 公网 IP(深圳移动,家庭 NAT;8.12 运营商重分配过一次) |
| `<OVL_HOMESFY>` / `<OVL_CLIENT>` | netbird overlay IP(homesfy / Windows) |
| `<MGMT_DOMAIN>` | netbird 自建管理域(管理/中继/信号统一入口) |
| `<SRV_USER>` / `<CLOUD_USER>` / `<LOCAL_USER>` | homesfy / acloud / 本地 Windows 用户名 |
| `<SING_BIRD_DIR>` | homesfy 部署目录(`~/servers/sing-bird`) |
| `<SINGBIRD_APP_DIR>` | Windows 安装目录 |

---

## 0. 环境与架构

| 角色 | 机器 | 网络 | 公网 IP | 角色说明 |
|------|------|------|---------|---------|
| 客户端 | Windows(深圳移动) | 家庭 NAT | `<CLIENT_WAN>`(8.12 运营商重分配) | singbird app:sing-box.exe(内含 netbird 引擎,netstack 模式,无内核 wg 网卡) |
| 服务端 | homesfy(惠州联通) | 家庭 NAT | `<HOME_SRV>` | sing-netbird 统一二进制(kernel_tun,wt0),部署于 `<SING_BIRD_DIR>/` |
| 中继/管理 | acloud(阿里云) | 公网 | `<ACLOUD>` | netbird-server(docker,mgmt+relay+STUN 3478)+ Caddy + frp 备用;配置 `~/servers/netbird/config.yaml` |
| 代理出口 | vmrack | VPS | `<PROXY_VPS>` | vless+Reality(两端 sing-box 的 `proxy` outbound) |
| 其他 peer | ddvps | VPS | `<PEER_VPS>` | 独立 netbird peer,后台保活,与本次问题无关 |

- SSH 路径:`ssh <SRV_USER>@homesfy` = `<OVL_HOMESFY>:12322`(netbird overlay IP,wt0)
- 隧道:singtun(172.19.0.1/30,auto_route)→ netbird 引擎 → WireGuard → 对端

---

## 1. 时间线

### 8.10 晚 —— 架构搭建(前置工作)

| 时间 | 事件 |
|------|------|
| 17:49 | sing-box `7eaa8304d` netbird kernel-TUN 数据通路(路线A,内核路由直连 wt0 绕过 sing-box TUN) |
| 17:58 | sing-box `1c12f7641` 双引擎共存流量机制文档化 |
| 19:03 | sing-box `551bd7c65` + singbird `79d46e8` pidfd Android workaround |
| 21:30 | sing-box `05ec963a1` + singbird `c3d838b` overlay 入站 TCP 桥接(expose_ports) |
| 21:37 | singbird `2381218` expose_ports 重启保留 |
| 22:27 | Windows 引擎启动(nb-engine.log 首条) |
| 23:10 | sing-box `2a0a32af7`(bridge 诊断)+ `3739fe5e7`(skills) |
| 23:12 | singbird `33ff92e` Expose Ports editor |

homesfy 部署(同日晚):config.json 15:17、sing-netbird 二进制 15:28、systemd 单元 15:45-15:59、进程 16:06 启动。

### 8.11 —— 问题报告与诊断(主调试日)

- 用户报告:SSH 到 homesfy 慢,**和走 frp 差不多**,打字延迟明显;按说应走 P2P 隧道
- 上午~中午:证据链建立(见 §2/§3)
- 下午 12:30-12:41:两端配置补丁 + 重启 → **首次修复成功**(RTT 44ms,SSH 0.7s)
- 下午:服务端 netbird.io 依赖清理(遥测+GeoLite2),期间发现并修复 **GeoLite2 下载卡死服务端启动**

### 8.12 —— 回归与二次修复

- 早晨:SSH 又回到 4.5~5s
- 根因:**运营商给 Windows 重分配公网 IP**,P2P 重新协商时 Windows 的代理出口污染候选复活(域名规则漏洞)
- 修复:Windows 补 `process_path <SINGBIRD_APP_DIR>\sing-box.exe → direct`(与 homesfy 对称)
- 最终验证:SSH 0.56~1.0s,隧道 RTT 16.9~22.6ms(avg 18.7ms,0% 丢包),两端端点均为真实公网 IP

---

## 2. 症状与实测数据(8.11)

| 指标 | 值 | 说明 |
|------|-----|------|
| SSH 建连(`ssh true`) | **4.0~5.3s** ×8 次 | 隧道路径 |
| SSH 建连(frp 对比) | **0.8~1.0s** ×5 次 | `<ACLOUD>:12323` → frp → homesfy |
| 隧道内 RTT(homesfy ping `<OVL_CLIENT>`) | **323~413ms,avg 368ms,0% 丢包** | 稳定但极慢 |
| 腿程(win→acloud / homesfy→acloud) | 23ms / 20ms | 直接 ping |
| 灌 4MB 下载 | 33s(≈123KB/s) | 吞吐与 350ms RTT 匹配 |
| ping overlay IP 本地回显 | <1ms,TTL=128 | 本地栈回显,测不出隧道(已知签名) |
| ICMP 到双方公网 IP | 100% 丢 | 家庭路由器丢弃,直连 RTT 无法用 ICMP 测 |

**关键推论**:SSH 建连 ≈10 个 RTT × 350ms ≈ 4-5s,与实测完全吻合。慢 = 路径 RTT 高,不是丢包。

---

## 3. 诊断过程(证据链)

1. **排除本地/引擎问题**:同样走 singtun、目标直连 acloud 的 frp 测试连接只要 643~855ms(Clash API 字节计数),隧道目标连接却 4~5s → 慢在网络路径,不在 TUN/DNS/引擎
2. **字节计数定位中继**:灌 4MB 流量前后对比 Clash API 连接表,`<ACLOUD>:3478`(UDP)下载字节 +4.5MB —— **数据走的是 acloud 的 TURN 中继,不是 P2P**
3. **引擎日志端点异常**:`configure WireGuard endpoint to: <PROXY_VPS>:46455`(代理出口 IP)—— 对端候选不是 homesfy 的真实公网 IP
4. **对称性证据**:homesfy 日志里同一时刻给 Windows 配的是正确端点 —— 说明 **homesfy 的 srflx 被污染、Windows 的没被污染**(homesfy 引擎是独立进程,流量进自己 TUN 后走代理;Windows 侧当时部分直连)
5. **服务端配置核实**:netbird-server config.yaml 只有 `stunPorts:[3478]`,无 turnConfig/stuns 覆盖 → 客户端使用**内置默认 STUN `stun.netbird.io:3478`**(自建服务端也豁免不了);引擎用自带 DNS 解析出 IP 后**直接按 IP 建连**,命中 `geosite-!cn → proxy` → 从代理 VPS 出去 → srflx 变成代理出口 IP

**根因链(完整)**:

```
netbird 引擎 socket(普通 socket,无 sing-box 的 fwmark 旁路标记)
  → auto_route 默认路由收进自己的 singtun TUN
  → sing-box 当普通应用流量按规则路由
  → STUN 探测(默认 stun.netbird.io,服务端未覆盖)→ !cn → vless 代理 → 代理出口
  → srflx 候选 = <PROXY_VPS>(代理出口 IP)而非真实公网 IP
  → ICE 用错误端点打洞必然失败
  → 回落到 acloud TURN 中继(3478),中继路径 RTT 350ms(腿程才 43ms,中继/容器/帧协议额外 ~300ms)
  → SSH ~10 RTT × 350ms ≈ 4-5s,"和 frp 差不多"(本质也是绕服务器中转,且更慢)
```

为什么 sing-box 自己的 vless/DNS 拨号不循环进 TUN:它们由 sing-box 自己的 dialer 创建,带 fwmark/旁路标记,ip rule 排除;netbird 库用自己的代码路径(wireguard-go bind / net.Dialer)创建 socket,不带标记 → 被当普通流量捕获。**对 sing-box 来说,引擎流量和普通应用流量没有区别。**

---

## 4. 修复内容(全部改动清单)

### 4.1 客户端路由规则(两端,均插在 `geosite-geolocation-!cn → proxy` 之前)

**homesfy** `<SING_BIRD_DIR>/config.json`(备份 `config.json.bak-*`):
```json
{ "process_path": ["<SING_BIRD_DIR>/sing-netbird"], "outbound": "direct" },
{ "domain_suffix": ["netbird.io", "<MGMT_DOMAIN>"], "outbound": "direct" }
```

**Windows** `<LOCAL_USER>\AppData\Roaming\io.nekohasekai.sfm\SingBird\{profiles.json, sing-box-config.json}`:
```json
{ "domain_suffix": ["netbird.io", "<MGMT_DOMAIN>"], "outbound": "direct" },      // 8.11 加
{ "process_path": ["<SINGBIRD_APP_DIR>\\sing-box.exe"], "outbound": "direct" }   // 8.12 补(关键!)
```

> **域名规则的漏洞(8.12 教训)**:引擎用自带 DNS 解析出 IP 后直接按 IP 建连(无域名上下文),`domain_suffix` 规则匹配不到 → 又掉进 proxy → 代理出口候选死灰复燃。**必须两端都用 process_path 规则**。进程规则只命中引擎的 TUN 自环 socket(sing-box 自己的 vless/DNS 拨号旁路 TUN 不受影响;普通 app 流量源进程不同也不受影响——process 规则匹配源进程,不破坏分流)。

### 4.2 客户端 DNS 规则(两端)

```json
{ "domain_suffix": ["<MGMT_DOMAIN>", "netbird.io"], "server": "dns-direct" }
```
原 `dns.final = dns-remote`(1.1.1.1 走代理),控制面 DNS 每 5 分钟过一次代理(实测 200~430ms);改后 223.5.5.5 直连(58ms),控制面不再依赖代理。

### 4.3 服务端 acloud `~/servers/netbird/config.yaml`(备份 .bak/.bak2)

```yaml
server:
  ...
  disableAnonymousMetrics: true   # 关闭 ingest.netbird.io 匿名遥测
  disableGeoliteUpdate: true      # 关闭 pkgs.netbird.io GeoLite2 下载(启动卡死根因)
```

> **GeoLite2 下载卡死启动**:服务端启动时 `autoUpdate` 会先访问 `pkgs.netbird.io` 查最新版本号再下载 mmdb,从中国网络 http=000 超时 → 整个服务卡在 init,STUN socket 建了但不应答(响应卡 tx_queue,连 docker-proxy 转发都正常但无响应)。`disableGeoliteUpdate: true` 后用本地已有 GeoLite2-City_*.mmdb,完全不联网,启动 4 秒完成。
>
> **不要配外部 `stuns:`**:会禁用本地 STUN 监听(`createSTUNListeners` 只在 `Relay.Stun.Enabled` 时起 3478 监听);stunPorts 本地模式会自动下发 `stun:<exposedHost>:3478` 给客户端。

### 4.4 重启操作

- homesfy:`sudo systemctl restart sing-bird`
- Windows:完全退出 singbird 托盘再打开(必须整个 app 重启——它启动时读 profiles.json 生成配置,只杀 sing-box.exe 会用内存旧配置重新生成,补丁丢失)
- acloud:`docker restart netbird-server`

### 4.5 服务端启动完成判据

```
docker logs netbird-server 出现:
  "Relay WebSocket handler added (path: /relay)"
  "STUN server listening on [::]:3478"
+ UDP 3478 STUN binding 有响应(注意:重启后要等 geolocation 检查完成,若卡住即 §4.3 问题)
```

---

## 5. 验证结果

| 指标 | 修复前(8.11) | 首次修复(8.11 下午) | 二次修复(8.12) |
|------|--------------|--------------------|----------------|
| SSH 建连 | 4.0~5.3s | 0.69~0.90s | **0.56~1.0s** |
| 隧道 RTT | 323~413ms | 19~127ms(avg 44ms) | **16.9~22.6ms(avg 18.7ms,0% 丢)** |
| homesfy→Windows 端点 | <PROXY_VPS>(代理出口) | <CLIENT_WAN>(旧) | **<CLIENT_WAN>(新):57862** |
| Windows→homesfy 端点 | <PROXY_VPS>(代理出口) | <HOME_SRV> | **<HOME_SRV>:38603** |
| wg 握手 | 10s 级 flap | 0.00~0.09s | 0.00s |
| 控制面 DNS | 200~430ms | 58ms | 58ms |
| netbird.io 全链路访问 | 有(stun/ingest/pkgs) | 无 | 无 |

---

## 6. 相关 sessions 与提交记录

### Sessions(本地 Hermes 会话库)

| session | 标题 | 内容 |
|---------|------|------|
| `20260810_131619_778ea4`(8.10 13:17,571 条) | 独立安装时流量如何分流 | 8.10 晚架构铺垫:双引擎流量机制、expose_ports 桥接、Android 配置恢复 |
| 本次会话(8.11~8.12) | SSH 慢速排查 | 本文档主内容 |
| `20260810_160945_80e28b` | 远程 Docker Compose 部署 RustDesk | 同日另一任务,无关 |

### 本地仓库提交(8.08~8.12)

**~/works/sing-box**(remote: 私有 `<PRIV_REPO>`,0 ahead):
```
3739fe5e7 08-10 23:10 docs: netbird inbound-bridge debugging skills + memory update
2a0a32af7 08-10 23:10 netbird: bridge conn timing diagnostics + fix Windows build
05ec963a1 08-10 21:30 feat: netbird overlay入站TCP桥接(expose_ports)
551bd7c65 08-10 19:03 fix: 恢复上游pidfd_android.go workaround
1c12f7641 08-10 17:58 docs: 项目memory更新 — 双引擎共存流量机制
7eaa8304d 08-10 17:49 feat: netbird kernel-TUN 数据通路(路线A)
```
未提交修改:`M .hermes/skills/netbird/netbird-overlay-debugging/SKILL.md`(本次排查新增 §6.5 候选污染 + §6 服务端排障,已脱敏)

**~/works/singbird**(remote: 私有 `<PRIV_REPO>`,**7 commits ahead,未推送**):
```
33ff92e 08-10 23:12 feat: Expose Ports editor + local target auto-fill
2381218 08-10 21:37 fix: netbird expose_ports 重启保留
c3d838b 08-10 21:30 feat: netbird expose_ports 配置支持
79d46e8 08-10 19:03 fix: Android<12不再跳过netbird引擎启动
c46104e 08-10 17:57 docs: Android旧版CA池坑入库
9afa71a 08-09 09:53 fix: 过滤页进程树行布局重构
4459f0d 08-08 17:08 docs: 发布工作流
```
未跟踪:`?? .hermes/skills/netbird/`

**~/works/netbird**:窗口内无提交(纯克隆,无源码改动)
**~/works/sing-netbird**:本地不存在

### homesfy ~/work/singbird —— **未合并!**

- HEAD 停在 `046b163`(8.08),**落后本地 singbird 7 个提交**
- remote 是旧的公开仓库地址(本地已切到私有 `<PRIV_REPO>`)
- 与部署无关:homesfy 线上部署在 `<SING_BIRD_DIR>/`(二进制来自 sing-box 仓库构建),不是 `~/work/singbird`
- 如需同步:`git push` 私有远端后 homesfy 上改 remote 或重新 clone

---

## 7. 经验教训

1. **P2P 不一定比中继快**:候选污染时 P2P 打不通,数据走中继且中继实现差时比 frp 还慢 5 倍。判断依据是实测路径,不是"应该走 P2P"
2. **引擎流量会被自己的 TUN 抓**:auto_route 在内核路由层接管,引擎 socket 无 fwmark 旁路 → 被当普通流量 → 规则送进自己的代理。对 sing-box 而言引擎=普通应用
3. **域名规则有 IP 直连漏洞**:引擎自带 DNS 解析后按 IP 建连,domain 规则匹配不到 → **进程规则(process_path)才是最稳的 bypass**
4. **netbird.io 依赖共 4 处**:stun(客户端默认 STUN)、ingest(遥测)、pkgs(地理库)、docs(纯文本);服务端不配 turnConfig 客户端就永远用公共 STUN
5. **服务端"重启后全挂"可能是假故障**:GeoLite2 下载卡死 init,STUN 无响应但 socket 在;判据看 docker logs
6. **运营商换 IP 会触发 P2P 重新协商**,残留污染候选会再次被 ICE 选中(半通隧道:单方向可达,SSH 秒级退化)——两端进程规则齐了之后可自动恢复直连
7. **判读速查**:隧道突然变慢 → 看两端引擎日志 `configure WireGuard endpoint to:` 地址:代理出口 IP=污染候选复活;真实公网 IP=正常直连
8. **敏感信息不入库**:本文档与相关技能均使用占位符,真实 IP/域名/用户名只存在于本地配置与项目记忆,提交前应复查

---

## 附录:当前生效配置快照(2026-08-12)

**Windows route.rules 顺序**:sniff → multicast/direct → dns hijack → 特例域名/direct → netbird.io+<MGMT_DOMAIN>/direct → **process_path sing-box.exe/direct** → !cn/proxy → cn/direct → geoip-cn/direct → dns/direct

**homesfy route.rules**:同上,但多一条 **process_path sing-netbird/direct**

**homesfy config 备份**:`<SING_BIRD_DIR>/config.json.bak-*`(3 份)
**acloud config 备份**:`~/servers/netbird/config.yaml.bak`、`.bak2`
