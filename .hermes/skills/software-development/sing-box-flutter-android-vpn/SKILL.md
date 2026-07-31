---
name: sing-box-flutter-android-vpn
description: Android VPN(VpnService+libbox)调试与构建加速 playbook — adb 日志定位、TUN fd 生命周期、接口监控、gradle vfs.watch 坑
---

# sing-box-flutter Android VPN 调试与构建

适用于 `~/works/sing-box-flutter`（Android 端 VpnService.kt + MainActivity.kt + BoxService.kt + libbox.aar）。

## 触发条件
- Android 上 VPN 不通 / 启动失败 / 停止无效 / 按钮状态不对
- gradle 构建异常慢（3 分钟以上）
- 需要 adb 抓日志定位引擎问题

## adb 调试入口

```bash
export PATH="$PATH:/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools"
# 关键日志（run-as 读取 app 私有目录）
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/markers.log    # Kotlin 侧流程日志
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/go_markers.txt # Go 侧启动日志
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/singbox.log         # 引擎运行时日志(ERROR 定位点)
# 系统侧 VPN 网络状态
adb shell "dumpsys connectivity | grep -c 'type: VPN'"
adb shell "ip route show table 99"     # fwmark VPN 路由表(空=丢包)
adb shell "ip rule | grep fwmark"
# clash API（引擎内部，绕 TUN）
adb forward tcp:19090 tcp:9090
curl -s http://127.0.0.1:19090/connections   # 活跃连接/流量
curl -s "http://127.0.0.1:19090/proxies/proxy/delay?timeout=8000&url=http://www.gstatic.com/generate_204"  # proxy 连通性
# UI 自动化（模拟点按钮）
adb shell uiautomator dump /sdcard/ui.xml && adb shell cat /sdcard/ui.xml | grep -oE 'content-desc="(Start|Stop)"'
adb shell input tap <x> <y>
```

注意：adb 传含 `/` 的路径参数会被 MSYS 路径转换污染，用 `MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL="*"` 或 base64 管道绕过。

## 已知坑（全部实测踩过）

### 1. VPN 权限对话框后 MethodCall 参数丢失
`startVpn` 走 `startActivityForResult(REQUEST_VPN)` 时，`onActivityResult` 里 `startVpnService(null, result)` 丢光 configDir/netbirdEnabled。
修复：缓存 `pendingStartVpnCall = call`，onActivityResult 恢复。

### 2. stopVpn 无效 / stop→start 后 VPN 网络不注册（最深的坑）
现象：点停止按钮反复调用但不停；或 stop→start 后 dumpsys 无 VPN 网络、`ip rule` fwmark 指向空 table 99、tun0 残留、app 流量丢包。
根因链：
- VpnService 被系统绑定（BIND_VPN_SERVICE，ConnectivityService 持有 binder），`stopSelf()/stopService()` **不触发 onDestroy**
- onDestroy 不跑 → `ParcelFileDescriptor` 不关 → VPN 网络残留 → 下次 `establish()` 注册异常
修复：`VpnService.shutdown()` **严格顺序**：
1. `currentTunFd?.close()`（关 fd → Android 移除 VPN 网络 → 系统解绑）
2. `boxService?.stop()`
3. `stopForeground(true)` + `stopSelf()`
MainActivity stopVpn 调 `SfVpnService.instance?.shutdown()`；onDestroy/onRevoke 也要 close fd + 置 null。

### 3. MIUI SELinux 挡 /proc/net → 必须实现接口监控
Android 10 MIUI 上 SELinux 阻止 /proc/net 读取，sing-box 自检接口失败 → 所有 outbound 报 `no available network interface`（连 rule-set 下载都失败）→ 可能触发 RemoteRuleSet.httpClient nil 竞态 SIGABRT。
修复（VpnService.kt）：
- `getInterfaces()`：用 `java.net.NetworkInterface` 构建 libbox `NetworkInterface` 列表（原来返回 null）
- `startDefaultInterfaceMonitor(l)`：ConnectivityManager `registerDefaultNetworkCallback` + **启动立即上报**（`cm.activeNetwork`），否则启动瞬间 rule-set 下载就失败
- 辅助类 `SimpleNetworkInterfaceIterator` / `EmptyStringIterator`（gomobile proxy 接口自己实现）

### 4. VPN 激活后默认接口变 tun0（自己）
ConnectivityManager 的 default network 在 VPN 激活后 = tun0。sing-box 的 selectInterfaces：排除 MyInterfaces(tun0) 后按默认接口 index 匹配 → 空 → 仍报 no available network interface。
修复：`reportDefaultInterface` 跳过 `tun*` 接口，遍历 `cm.allNetworks` 找有 `NET_CAPABILITY_INTERNET` 的物理接口（wlan0）上报。

### 5. Kotlin API level vs minSdk=24
- `NetworkCapabilities.isMetered` 是 **API 29** → 用 `hasCapability(NET_CAPABILITY_NOT_METERED) == false`（API 21）
- `java.net.NetworkInterface.isMulticast` 是 **API 25** → 删掉（接口 flags 只要 UP/RUNNING/LOOPBACK）
- 编译报 `Unresolved reference` 就是这个原因，先查 API level 再查拼写

### 6. 引擎日志定位
- `no available network interface` = 接口/路由问题（看坑 3/4）
- `unknown version: 72` = 下载链路通但内容非 srs（jsdelivr 返回了 HTML/错误页），不是网络问题
- `dns: exchanged A ...` = DNS 链路通；clash API connections 有数据流动 = 端到端通
- 配置占位 uuid 也能验证链路（握手失败 vs 接口错误可区分）

## 构建加速（gradle 8.14 vfs.watch 坑）

**现象**：所有 gradle 构建（任何项目/daemon/offline 模式）固定 3-4 分钟，CPU ~0，daemon 收命令后静默无 worker，profile 显示 Startup 占 87%。
**根因**：gradle 8.14 默认开启 file system watching（VFS），在这台 Windows 上挂起。
**修复**（android/gradle.properties）：
```
org.gradle.vfs.watch=false
```
效果：3m31s → 23s（14 倍）。`--no-watch-fs` 可快速验证（空项目 5s vs 3-4min）。
**排查路径**（复用）：`--profile` 看 Startup → 空 Groovy 项目对照 → `--offline`/`--no-daemon`/干净环境/降版本逐一排除 → `--no-watch-fs` 一击命中。
另：`--no-daemon` 在 gradle.properties 有 jvmargs 时会被忽略（single-use daemon），不算真正绕过。

## 环境事实
- 设备：Redmi Note 7 Pro (violet, Android 10 MIUI)，adb 连接
- Defender 实时防护已关（构建提速贡献小，vfs.watch 才是主因）
- 构建命令：`puro flutter build apk --debug`（bash PATH 里的 flutter 是 default 环境，用 puro 的）
