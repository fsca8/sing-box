---
name: sing-box-flutter-android-vpn-debug
description: Android VPN 调试与修复 —— adb 日志位置、VpnService 常见坑、interface monitor 实现。sing-box-flutter 项目 Android 端调试专用。
---

# sing-box-flutter Android VPN 调试

## 触发条件
- 启动 VPN 后按钮状态不变 / VPN 不生效 / 引擎崩溃（SIGABRT）
- 需要查看 Android 侧引擎日志

## adb 日志位置（包名 io.nekohasekai.sfm.sing_box_flutter）

```bash
export PATH="$PATH:/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools"
# Kotlin 侧 markers（启动流程/心跳）—— 被轮转覆盖，看 head+tail
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/markers.log | grep -v Heartbeat
# Go 侧 markers（openTun/StartOrReloadService）
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/go_markers.txt
# 引擎运行时日志（配置里 "log": {"output": "singbox.log"}）
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/singbox.log
# logcat（崩溃栈）
adb logcat -d | grep -iE 'sing-box|Marker|Go'
```

## 已确认的坑（2026-07-31 实测）

### 1. 权限对话框后 MethodCall 参数丢失
`startVpn` 首次需要 VPN 权限 → `pendingStartVpnResult = result` 挂起 →
`onActivityResult` 里 `startVpnService(null, result)` **call 为 null** →
configDir/netbirdEnabled 全丢 → 引擎空配置启动。
修复：缓存 `pendingStartVpnCall`，恢复时传入。

### 2. stopVpn 的 stopSelf() 无效
前台 START_STICKY 服务下 stopSelf() 被系统延迟/忽略，onDestroy 不触发。
修复（三步强制停止）：
1. `boxService?.stop()` 直接停引擎
2. `stopForeground(true)` + `stopSelf()`
3. Activity 侧 `stopService(Intent(...))` 保证 onDestroy

### 3. TUN interface_name 陷阱
Android VpnService 忽略 tun 的 `interface_name`（系统分配 tun0）。
配置里写死 "singtun" → sing-box 按名字排除自己的 TUN 失败 →
把 tun0 当默认接口 → outbound 死循环。
启动前注入：移除 tun inbound 的 interface_name + 确保 auto_detect_interface=true。

### 4. MIUI SELinux 阻止 /proc/net → outbound 全挂（最关键）
报错特征：`router: process DNS packet: no available network interface`
（连 rule-set 远程下载都失败），随后可能 SIGABRT
（RemoteRuleSet.httpClient nil 竞态 panic）。
根因：sing-box 无法自行检测网络接口，VpnService 的
`getInterfaces()` 返回 null + `startDefaultInterfaceMonitor()` 空实现。
修复（VpnService.kt，commit 4451b27）：
- `getInterfaces()`: java.net.NetworkInterface 构建 LibboxNetworkInterface 列表
  （index/mtu/name/flags/type），Addresses/DNSServer 传 EmptyStringIterator（Go 侧 nil 安全）
- `startDefaultInterfaceMonitor(l)`: ConnectivityManager
  registerDefaultNetworkCallback + **启动立即上报当前默认接口**
  （cm.activeNetwork → reportDefaultInterface）
- `closeDefaultInterfaceMonitor`: unregisterNetworkCallback
- flags 用 syscall IFF_* 常量（IFF_UP=0x1, IFF_RUNNING=0x40, IFF_LOOPBACK=0x8, IFF_MULTICAST=0x1000）

### 5. Dashboard 按钮状态（Android）
`_ctrl.isRunning` 查 Dart Process，Android 上恒 null。
必须轮询 `_androidVpn.getVpnStatus()`（3 秒 Timer，initState 启动/dispose 取消）。

### 6. 启动时序：build 先于 ProfileStore.load
`instance()` 首次创建时 ProfileStore 未 load → _configContent=null → 按钮灰。
`_init()` 在 `await ProfileStore().load()` 后必须调 `_ctrl.reloadFromStore()`。

## libbox.aar 编译（含 netbird）
- 脚本在 sing-box-flutter 仓库：`bash .hermes/skills/sing-box-flutter/scripts/rebuild-libbox.sh`
  （不在 sing-box 目录！）
- build tags 必须含 `with_netbird`（否则 BoxService 的 NetbirdStartAll 不存在）
- **不能 GOWORK=off**：netbird 只通过 go.work 引用（go.mod 无 require），
  off 时 `go list -m github.com/netbirdio/netbird` 报 "not a known dependency"
- NDK 29.0.14033849，GOMAXPROCS=4，ANDROID_HOME 用原生路径
- 产物复制到 android/app/libs/libbox.aar（build.gradle.kts:47 引用）
