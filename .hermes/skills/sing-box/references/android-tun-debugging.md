# Android TUN 调试（MIUI Android 10 实测 2026-07-31）

环境：Redmi Note 7 Pro / Android 10 / MIUI 12.5（adb 470f6855）。
Supersedes 旧的 "mixed stack works" 结论。

## 1. TUN stack 必须 gvisor（不是 mixed）

`stack: mixed` 在 Android 10 MIUI 上 **gvisor TCP 半栈不启动**：DNS/UDP 通（system 半栈）、TCP SYN 到达 tun0（`/proc/net/dev` RX 增长）但引擎不处理 → 连接超时（YouTube 打不开）。判定：引擎启动**无 `started at tun0` 日志**；`dns: exchanged` 正常但 `outbound connection` 从不出现。

正确组合：`stack=gvisor` + `route.auto_detect_interface=false` + 无条件 protect()（common/dialer/default.go Android 分支）。

## 2. 配置注入必须下沉到 Kotlin

MainActivity.onResume auto-start **不经过 Flutter 的 _injectAndroidConfig**。注入点：`VpnService.onStartCommand` 读配置后、启动引擎前，用 org.json.JSONObject 强制：`stack=gvisor`、移除 `interface_name`（VpnService 分配 tun0）、`auto_detect_interface=false`。

## 3. 默认接口排除自身 TUN

VPN 激活后 ConnectivityManager 默认网络 = tun0（自己）→ sing-box selectInterfaces 排除 MyInterfaces(tun0) 后按默认接口 index 匹配 → 空 → "no available network interface"。`reportDefaultInterface` 跳过 tun* 接口，遍历 allNetworks 找有 INTERNET capability 的物理接口（wlan0）上报。

## 4. stopVpn 必须先关 TUN fd（系统绑定陷阱）

VpnService 被系统绑定（BIND_VPN_SERVICE）→ stopSelf/stopService **不触发 onDestroy** → fd 残留 → 下次 establish 失败。`shutdown()` 顺序：close ParcelFileDescriptor（移除 VPN 网络→系统解绑）→ boxService.stop() → stopForeground(true) → stopSelf()。onDestroy/onRevoke 同样 close fd。

## 5. SQLite monitor 接线

- libbox `NewMonitorService(dbPath)` 设 globalCollector → 引擎 hooks 自动 `monitor.Get().Record*` 写 SQLite（**创建一次即自动记录**）
- Kotlin BoxService 必须显式创建（声明字段不创建 → 查询恒 "[]"）
- **`GetConnectionHistory` 只读 connBuf（RecordTCP 从不 push）→ 必须改查 `QueryConnections(0, limit)`**；`GetDNSHistory` 同理 `QueryDNS`
- Windows /monitor/* HTTP 端点 = 二进制内嵌 collector + api.go；Android 走 MethodChannel

## 6. Live Monitoring 全平台（Clash API 轮询）

Android 引擎在 app 进程内监听 127.0.0.1:9090（loopback 不走 TUN 可达）——Flutter 的 HTTP 轮询（/connections 标准端点，1s 周期）全平台通用。Kotlin EventChannel 只发 status 事件，连接数据必须 HTTP 轮询。

## 7. Per-App Filter 持久化

`updateAppFilter` 只改 companion 内存 → 重启丢失。写 SharedPreferences("vpn_filter_prefs") + onCreate `loadPersistedFilter` 恢复。UI 空选择必须可保存（= 关闭，clearAppFilter）。

## 8. 版本注入 / proto 冲突（见主 skill）

netbird commit 必须 -X 注入（`main.` 前缀 for cmd、完整路径 for libbox）；protobuf daemon 冲突改 sing-box `sbox.daemon`。

## adb 调试要点

- 二进制必须 `adb exec-out`（`adb shell` 文本模式损坏 SQLite → "database disk image is malformed"）
- MSYS 路径转换污染 adb 参数 → `MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL="*"` + base64 管道写 app 文件
- 流量物理验证：`grep tun0 /proc/net/dev` 前后 RX 对比
- 路由诊断：`ip route show table 99`、`dumpsys connectivity | grep 'type: VPN'`、`ip rule | grep fwmark`
- clash API：`adb forward tcp:19090 tcp:9090` + curl（9090 被占换端口）
