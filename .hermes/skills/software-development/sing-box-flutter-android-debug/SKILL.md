---
name: sing-box-flutter-android-debug
description: Android VPN runtime debugging for sing-box-flutter (adb markers/logcat, platform interface gaps, gradle build slowdowns). Use when VPN not working on device, engine crash/SIGABRT, or gradle build takes minutes.
---

# sing-box-flutter Android 调试与构建加速

设备调试 + 构建坑速查。适用于 `~/works/sing-box-flutter`（Flutter 客户端 + libbox Go 引擎）。

## 1. adb 抓日志（崩溃安全）

```bash
export PATH="$PATH:/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools"
adb devices   # 确认设备
# 应用私有目录（不需要 root）：
adb shell run-as io.nekohasekai.sfm.sing_box_flutter ls files/logs/
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/markers.log   # Kotlin侧标记(轮转,启动日志可能被心跳覆盖)
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/go_markers.txt # Go侧标记
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/singbox.log        # Go引擎日志(配置log.output指定)
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/CrashReport-sing-box.log
```

`markers.log` 会被 2 秒心跳刷屏（VpnService 的 heartbeat），先 `grep -v Heartbeat`。

## 2. 关键系统检查

```bash
adb shell ip addr          # 有无 tun 接口（openTun OK 但接口不存在 = 已被撤销/僵尸）
adb shell ip route         # 有无 0.0.0.0/0 dev tun
adb shell dumpsys connectivity | grep -i vpn   # 系统是否注册 VPN 网络
```

## 3. 已踩过的坑（2026-07 实测）

### 3.1 VPN 权限对话框后参数丢失
`startVpn` 弹权限框时 `pendingStartVpnResult=result` 挂起，onActivityResult 里曾调 `startVpnService(null, result)` → configDir/netbirdEnabled 全丢 → VpnService 读不到配置、引擎空配置启动。**修复：缓存 MethodCall**（`pendingStartVpnCall = call`），授予后 `startVpnService(pendingCall, result)`。

### 3.2 stopVpn 无效
`vpn.stopSelf()` 对前台 START_STICKY 服务被系统延迟/忽略。**三步强制**：
```kotlin
boxService?.stop()   // 1. 直接停引擎
vpn.stopForeground(true); vpn.stopSelf()  // 2. 退前台+请求
stopService(Intent(this, VpnService::class.java))  // 3. Activity侧强制销毁
```

### 3.3 outbound 全挂 "no available network interface" + SIGABRT
根因链：MIUI Android10 SELinux 挡 `/proc/net` → sing-box 无法自行检测接口 → 必须平台上报。VpnService 曾空实现：
- `getInterfaces()` 返回 null → `NetworkInterfaces()` 空
- `startDefaultInterfaceMonitor(l) {}` 空 → `DefaultInterface()` nil

`selectInterfaces()`（common/dialer/default_parallel_interface.go:225）拿不到接口 → 所有 outbound 失败；RemoteRuleSet 的 `StartContext` 失败后 httpClient 仍 nil → updater fetch 竞态 panic（SIGABRT）。

**修复（VpnService.kt）**：
- `getInterfaces()`：`java.net.NetworkInterface.getNetworkInterfaces()` 构建 `LibboxNetworkInterface`（index/mtu/name/flags/type），跳过 `!isUp`
- `startDefaultInterfaceMonitor()`：`ConnectivityManager.registerDefaultNetworkCallback` + **启动立即上报** `cm.activeNetwork`（rule-set 下载在启动瞬间发生，等回调就晚了）
- `reportDefaultInterface`：`LinkProperties.interfaceName` + index + metered/constrained → `l.updateDefaultInterface(...)`
- gomobile proxy 迭代器：自己实现 `NetworkInterfaceIterator`/`StringIterator` 接口类

### 3.4 Kotlin minSdk=24 的 API 级别坑（编译报 Unresolved reference）
- `NetworkCapabilities.isMetered` = **API 29** → 用 `hasCapability(NET_CAPABILITY_NOT_METERED) == false`（API 21）
- `java.net.NetworkInterface.isMulticast` = **API 25** → 删掉该 flag
- 改 Kotlin 后若编译报 Unresolved reference，先查 API level（compileSdk=36 也会按 minSdk 拦截）

### 3.5 tun interface_name 不能设
配置 `interface_name: "singtun"` 而 VpnService 实际分配 tun0 → sing-box 按名字排除自己的 TUN 失败 → 把 tun0 当默认接口 → 死循环。**Android 启动前注入**：移除 tun `interface_name` + 确保 `route.auto_detect_interface=true`（dashboard `_injectAndroidConfig()`）。

### 3.6 Dashboard 按钮状态（Android）
`_ctrl.isRunning` 查 Dart Process，Android 上引擎在 Kotlin 侧恒 null → 按钮永远灰/不更新。**轮询** `getVpnStatus()` 每 3 秒更新 `_vpnRunning`。

### 3.7 netbird enable 开关传递
全链路：`startVpn(netbirdEnabled:)` → MainActivity intent extra → VpnService 读开关 → **关闭时不读 netbird-config.json**（nbConfig=null → BoxService 跳过 NetbirdStartAll）。

## 4. gradle 构建慢（3-4 分钟 → 23 秒）

**根因**：gradle 8.14 默认开启 file system watching（VFS），Windows 上每次构建挂起 3-4 分钟（CPU ~0，daemon 收命令后静默、无 worker 线程、client 等 daemon 响应死循环）。

**修复**：`android/gradle.properties` 加：
```properties
org.gradle.vfs.watch=false
```

**排查路径（可复用）**：
1. `gradlew assembleDebug --profile` → 看 Startup 是否占大头（本机 Startup 3m14s / 总 3m42s）
2. 空目录最小 Groovy 项目 `gradle help` 对照 → 排除项目/插件/Kotlin DSL
3. `--offline` / `--no-daemon` / 干净 PATH / 降级 8.12 逐一排除
4. **`--no-watch-fs` 验证**：5 秒完成 = 破案
5. 注意：`--no-daemon` 在设置了 jvmargs 时被忽略（single-use daemon）

其他已确认**非**主因：Defender 实时扫描、CPU（i7-1065G7 弱但非瓶颈）、磁盘（NVMe）、网络/代理、DNS、JBR 21。

## 5. 编译产物链路

- libbox.aar（含 netbird）：`rebuild-libbox.sh`（sing-box-flutter 仓库 `.hermes/skills/sing-box-flutter/scripts/`），必须 `with_netbird` tag 且**不能 GOWORK=off**（netbird 仅 go.work 引用）；`-target android/arm64` 单架构
- 产物 → `android/app/libs/libbox.aar`，`build.gradle.kts:47` `implementation(files(...))`
