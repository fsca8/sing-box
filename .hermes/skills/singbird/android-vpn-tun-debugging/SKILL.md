---
name: android-vpn-tun-debugging
title: Android VPN TUN 调试 — sing-box libbox 真机修复
description: Android 10 MIUI 上 sing-box VpnService TUN 调试：stack 选择、auto-start 注入、系统绑定、MonitorService 接线、adb 验证、代理不生效诊断阶梯。
---

# Android VPN TUN 调试（Redmi Note 7 Pro / Android 10 MIUI 实测 + MI PAD 4 PLUS / Android 8.1）

## 环境
- Xiaomi Redmi Note 7 Pro, Android 10 (API 29), MIUI, kernel 4.14
- SELinux 挡 /proc/net → 接口检测受限
- 设备: 470f6855; adb: `export PATH="$PATH:/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools"`
- MI PAD 4 PLUS (clover), Android 8.1.0 (API 27): adb 全路径 `/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools/adb.exe`

## 核心结论（2026-07-31 实测验证）

### 1. TUN stack 必须是 gvisor（不是 mixed！）
- `mixed`（默认）在 Android 10 MIUI 上 **gvisor TCP 半栈不启动**：DNS/UDP 通（system 半栈）、TCP SYN 到达 tun0（`/proc/net/dev` RX 增加）但引擎不处理 → 连接超时（YouTube 打不开、nc 超时）
- **正确组合：`stack=gvisor` + `route.auto_detect_interface=false` + 无条件 protect()**
- 引擎日志确认：gvisor 正常时有 `inbound/tun[tun-in]: started at tun0`；mixed 故障时该行缺失（TUN inbound 部分启动失败）
- 诊断信号：`dns: exchanged` 正常但 `outbound connection` 从不出现 = TCP 半栈死了

### 2. 配置注入必须下沉到 Kotlin（auto-start 不经过 Flutter）
- MainActivity.onResume auto-start 直接读 sing-box-config.json 启动引擎，**不走 Flutter 的 _injectAndroidConfig**
- 注入点：`VpnService.onStartCommand` 读配置后、启动引擎前，用 org.json.JSONObject 强制：`stack=gvisor`、移除 `interface_name`、`auto_detect_interface=false`
- 只改 Flutter 侧 = auto-start 路径永远用原始配置

### 3. 默认接口必须排除自身 TUN
- VPN 激活后 ConnectivityManager 的 default network 变成 tun0（自己）→ sing-box selectInterfaces 排除 MyInterfaces(tun0) 后按默认接口 index 匹配 → 空 → "no available network interface"
- `reportDefaultInterface` 跳过 tun* 接口，遍历 allNetworks 找有 INTERNET capability 的物理接口（wlan0）上报

### 4. stopVpn 必须先关 TUN fd（系统绑定陷阱）
- VpnService 被系统绑定（BIND_VPN_SERVICE，ConnectivityService 持有 binder）→ stopSelf/stopService **不触发 onDestroy** → fd 不关 → 下次 establish 时 VPN 网络注册失败
- 顺序：`shutdown()`：close ParcelFileDescriptor（移除 VPN 网络→系统解绑）→ boxService.stop() → stopForeground(true) → stopSelf()

### 5. MonitorService 接线（SQLite 历史）
- libbox `NewMonitorService(dbPath)` 设 globalCollector，引擎 common/dialer、common/tls、dns/client 的 hooks 自动 `monitor.Get().Record*` 写 SQLite——**创建一次即自动记录**
- Kotlin BoxService 必须显式创建（之前声明了字段但从未创建 → 查询恒 "[]"）
- **坑**：`GetConnectionHistory` 只读 connBuf（ringbuf，RecordTCP 从不 push）→ 恒空；必须改查 `QueryConnections(0, limit)`（SQLite 权威存储）。`GetDNSHistory` 同理改 `QueryDNS`
- Windows 的 /monitor/* HTTP 端点是 sing-netbird 二进制内嵌 collector + api.go；Android 走 MethodChannel

### 6. Live Monitoring 全平台（Clash API 轮询）
- Android 引擎在 app 进程内监听 127.0.0.1:9090（loopback 不走 TUN，Flutter HTTP 可达）
- `MonitorServiceWindows`（类名历史遗留）的轮询逻辑全平台通用：/connections 标准端点 + 1s 周期
- Kotlin 只发 status 事件（EventChannel），连接/路由事件无数据源 → 必须用 HTTP 轮询

### 7. Per-App Filter 持久化
- `updateAppFilter` 只改 companion 内存 → 进程重启丢失。写 SharedPreferences("vpn_filter_prefs")，onCreate 时 `loadPersistedFilter` 恢复
- UI：空选择必须可保存（= 关闭 filter，调 clearAppFilter）；bottomNavigationBar 常驻

### 8. 权限 dialog 后 call 丢失（pendingStartVpnCall）
- VPN 授权 dialog 弹出期间 Flutter 发起的 startVpn MethodCall 会被系统打断（activity 暂停）→ 授权返回后 call 丢失，引擎不启动
- 解法：Kotlin 侧缓存 `pendingStartVpnCall`，onActivityResult/授权回调后补发

### 9. 按钮状态轮询 + minSdk 限制
- 按钮"已连接/未连接"状态不靠回调，轮询 `getVpnStatus`（引擎真实状态为准）
- **minSdk=24**：代码禁用 API 29+ 方法（VpnService.Builder 新 API 等），否则低版本设备 crash

### 10. TUN 下 mDNS/局域网发现失效（2026-08-05 根因确认）
- **现象**：开 TUN 后 LocalSend 找不到同网设备；route rule（`ip_cidr: 224.0.0.0/4` + `port: 5353` → direct）在 **Windows 有效、Android 无效**
- **机制**：route rules 只能管**发送路径**（包进 TUN 后怎么转发），管不了内核的**组播投递**（响应包投给谁）
  - Android：VpnService 建立后是系统 **default network** → 应用 socket 的 IP_ADD_MEMBERSHIP 走默认路由进了 tun0，**组成员关系锚定在 tun0**；其他设备的 mDNS 响应组播到达 wlan0，内核只投递给"在 wlan0 入组"的 socket → 响应永远收不到。规则再对也没用（接收路径不由 sing-box 决定）
  - Windows：wintun 只是路由表多一条默认路由，系统默认网络仍是物理网卡 → 组播加入/回包投递全走物理网卡；配 `auto_detect_interface=true` 时 direct 自动绑物理网卡，双向全通
- **正解：per-app 排除**（内核级绕过，不进 TUN）：App Filter 把目标 app 加入 disallowed → `addDisallowedApplication()`（VpnService.kt:389）→ 组播加入和回包全在 wlan0
  - LocalSend 包名 `org.engsteam.localsend`；filter 变更走 rebuildVpn() 会重启服务，需重新连接
  - sing-box 配置 `tun.exclude_package`（option/tun.go:45）语义等价，但本 app 的 Kotlin builder 自实现、只读 SharedPreferences filter，**不走配置项**——用 App Filter UI
- **验证手法**：引擎日志开 debug，224.0.0.251:5353 命中 rule 且 direct 出去但仍不可见 = 坐实回包投递问题
- 相关坑：Android 强制注入 `auto_detect_interface=false`（VpnService.kt:211）→ direct 不绑接口，只靠 protect(fd) 出物理网卡，发送路径脆弱

## adb 调试要点
- **读引擎日志**：`adb shell run-as <pkg> cat files/logs/{markers,go_markers}.txt` + `singbox.log`（app 私有目录，须 run-as）
- **二进制文件必须 `adb exec-out`**（`adb shell` 文本模式损坏 SQLite/二进制 → "database disk image is malformed"）
- MSYS 路径转换污染 adb 参数 → `MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL="*"` + base64 管道写入 app 文件
- 验证流量是否物理到达 tun0：`grep tun0 /proc/net/dev` 前后 RX 对比
- 路由诊断：`ip route show table 99`（fwmark VPN 表）、`dumpsys connectivity | grep 'type: VPN'`、`ip rule | grep fwmark`
- clash API：`adb forward tcp:19090 tcp:9090` + curl 127.0.0.1:19090/connections（9090 被占时换端口）

## 代理不生效诊断阶梯（2026-08-10 MI PAD 4 PLUS/Android 8.1 实测）

症状：VPN 已建立（tun0 有 0.0.0.0/0 路由、dumpsys type: VPN CONNECTED），国内正常、国外全挂。按序隔离故障层：

1. **先分 DNS vs 隧道**：Clash API `/dns/query?name=www.google.com`。返回 message 错误 = DNS 上游挂；返回 Answer = DNS 正常。
2. **隧道通判据**：`curl --resolve www.cloudflare.com:443:<CF-IP> https://www.cloudflare.com/` 经隧道访问 Reality 借用目标 → **HTTP 403 = 隧道通**（403 是 CF 拒 curl UA，TLS+HTTP 已通）。连不上才是隧道问题。
3. **绕过系统 DNS 直连**：`curl --resolve www.google.com:443:<真实IP> https://www.google.com/` → 200 = 隧道+代理全通，问题锁死在 DNS。
4. **x509 unknown authority 根因**：DoH 上游证书链根不在设备系统 CA 池。对比法：PC 上 `openssl s_client -connect <ip>:443 -servername <host> -showcerts` 看链根，再 `adb pull /system/etc/security/cacerts/ C:/...`（**须 Windows 原生路径**，MSYS /c/ 路径 adb 不认）+ 逐文件 `openssl x509 -noout -subject` 扫池。
   - Android 8.1 池 135 根（2018 冻结）→ **无 SSL.com Root CA ECC** → 1.1.1.1 DoH(cloudflare-dns.com) 挂；**8.8.8.8 DoH 链根 GTS R4→GlobalSign（池里有）可换**。
   - 修复：设备上改 `files/profiles.json`（持久，转义 JSON）+ `files/sing-box-config.json`（运行，紧凑 JSON）两处，base64 管道写回 run-as。
5. **改完仍挂 = 系统 resolver 层**：`dumpsys connectivity | grep 'Active default network'`——若 VPN Score=0 且默认网络仍是 wlan0，DNS 查询不进 TUN。**飞模切换后常见**；须 app 内完全关→开重建 VPN（飞模只重置物理网络，无效）。

## 非 debug 包读私有数据 / 覆盖安装

- **release 无 DEBUGGABLE → run-as 拒绝**。读配置/日志两条路：
  - 装 debug 版覆盖（release 也用 debug 签名 → 覆盖不丢数据；versionCode 须 ≥ 已装 `--build-number NNNN`，否则 `adb install -r -d` 降级装）
  - 或 `adb backup -f app.ab -noapk <pkg>`（设备确认弹窗）→ 解包：header 是 **4 行 \n 分隔**（非 \0）+ zlib 解压 + tar 提取 → `apps/<pkg>/f/monitor.db`
- 配置双文件都改才生效：`profiles.json`（app 持久化，转义嵌套 JSON）+ `sing-box-config.json`（引擎运行版，紧凑 JSON）；app 重启会从 profiles 重新生成后者
- 连接历史大量 error 排查：`adb backup` 拉 monitor.db，`SELECT error,COUNT(*) ... GROUP BY error`——单目标高频 i/o timeout = 该服务器端口不可达（如 RustDesk 21114 挂而 21116 通），非代理问题

## 其他坑
- gomobile 构造函数命名：Go `NewMonitorService` → Kotlin `MonitorService(dbPath)`（javap 确认签名）
- 引擎进程 fd 列表 `/proc/<pid>/fd` 用 adb shell 读不到（同 uid 限制）
- 构建：gradle 8.14 vfs.watch 在 Windows 挂起 3-4 分钟 → `org.gradle.vfs.watch=false`（3m31s→23s）
