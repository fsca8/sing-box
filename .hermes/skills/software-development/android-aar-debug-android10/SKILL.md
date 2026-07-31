---
name: android-aar-debug-android10
title: Android AAR Debug — Android 10 MIUI SELinux
description: Debug sing-box Android AAR on Android 10 (MIUI). SELinux /proc/net/ blocking, TUN routing, tun stack selection (gvisor vs mixed), VPN lifecycle fixes.
---

# Android AAR Debug - Android 10 MIUI

## Environment
- Xiaomi Redmi Note 7 Pro, Android 10 (API 29), kernel 4.14
- SELinux blocks read on /proc/net/tcp, /proc/net/route
- auto_detect_interface:true → "no available network interface"
- AAR build: go run ./cmd/internal/build_libbox -target android -platform android/arm64

## Key Fixes

### Always add protect() to Android sockets
common/dialer/default.go: after markFunc block, add:
```
if C.IsAndroid && networkManager != nil {
    protectFunc := networkManager.ProtectFunc()
    if protectFunc != nil {
        dialer.Control = control.Append(dialer.Control, protectFunc)
        listener.Control = control.Append(listener.Control, protectFunc)
    }
}
```

### Implement getInterfaces() + startDefaultInterfaceMonitor (Kotlin)
MIUI SELinux blocks /proc/net so sing-box cannot enumerate interfaces.
VpnService must report them via PlatformInterface:
- getInterfaces(): iterate java.net.NetworkInterface, build libbox
  NetworkInterface list (index/mtu/name/flags/type; Addresses can be
  EmptyStringIterator — Go side tolerates nil/empty)
- startDefaultInterfaceMonitor(l): ConnectivityManager
  registerDefaultNetworkCallback + **report cm.activeNetwork immediately**
  (startup is when rule-set downloads & first outbounds happen)
- closeDefaultInterfaceMonitor: unregister callback
- iterators: implement libbox NetworkInterfaceIterator / StringIterator
  interfaces in Kotlin (SimpleNetworkInterfaceIterator/EmptyStringIterator)

### Default interface must EXCLUDE own TUN
After VPN activates, ConnectivityManager's default network becomes tun0.
sing-box selectInterfaces: excludes MyInterfaces(tun0) then matches default
index → empty → "no available network interface" on ALL outbounds.
Fix: reportDefaultInterface skips tun* ifaces, scans allNetworks for a
physical network with NET_CAPABILITY_INTERNET and reports that (wlan0).

### Config injection must live in Kotlin (covers auto-start)
Flutter-side injection only runs on manual start; MainActivity.onResume
auto-starts VpnService directly with the raw file config. Inject in
VpnService.onStartCommand after reading sing-box-config.json:
- tun stack=gvisor (see stack table below)
- remove tun interface_name
- route.auto_detect_interface=false

### stopVpn: system-bound service never dies from stopSelf()
VpnService is system-bound (BIND_VPN_SERVICE, ConnectivityService holds
binder) — stopSelf()/stopService() do NOT trigger onDestroy → stale fd +
tun0 → next establish() fails to register VPN network (dumpsys shows no
VPN, app traffic hits empty fwmark table → no internet).
Fix: VpnService.shutdown() = close TUN ParcelFileDescriptor FIRST (removes
VPN network → system unbinds) → stopForeground → stopSelf. Same fd close
in onDestroy/onRevoke. Order matters.

### VPN permission dialog: cache the MethodCall
startVpn goes through prepare() dialog; onActivityResult previously called
startVpnService(null, result) losing configDir/netbirdEnabled. Cache
pendingStartVpnCall and restore it.

## Config (working for TCP on this device)
auto_detect_interface: false, stack: gvisor, rules: hijack-dns + dns:direct, final: proxy

## tun stack table (Android 10 MIUI实测)
| stack | 结果 |
|-------|------|
| system | UDP受限(需原始socket), 不可用 |
| mixed  | TCP死: gvisor半栈不启动(无"started at tun0"日志), DNS/UDP通 → 连接超时 |
| gvisor | ✅ 全通(TCP+UDP) |

## Debug tips
- adb: run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/markers.log / singbox.log
- "started at tun0" 缺失 = tun stack 没启动 (mixed 的 TCP 半栈问题)
- nc 前后 /proc/net/dev 的 tun0 RX 增量判断 SYN 是否物理到达 TUN
- dashboard 实时数据: Clash API 轮询 127.0.0.1:9090 (Android 引擎在进程内,
  loopback 不走 TUN 可达); /monitor/* 历史端点是 Windows 后端专有,
  Android 走 MethodChannel query*

## Known Limitations
- auto_detect_interface:true → SELinux blocks /proc/net (unless
  getInterfaces+monitor implemented; still keep false with protect())
- bind_interface/exclude_route → unknown fields in this AAR
- geosite/geoip rules → removed in sing-box 1.12+
