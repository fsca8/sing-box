---
name: android-vpn-tun-working-config
title: Android TUN Working Config & Troubleshooting (MIUI 10)
description: Verified working sing-box Android TUN config (stack=gvisor, auto_detect=false), Kotlin-side injection to cover auto-start, VPN stop order, and device-side TUN-path debugging commands.
---

# Android TUN Working Config & Troubleshooting

Supersedes the outdated "mixed stack works" note in android-aar-debug-android10.
All verified on Redmi Note 7 Pro / Android 10 / MIUI 12.5, 2026-07-31
(YouTube http=200 via VPN).

## Working config (force-injected on Android)
```
inbounds[].tun.stack: gvisor        # NOT mixed
tun.interface_name: REMOVED         # VpnService assigns tun0
route.auto_detect_interface: false  # MIUI SELinux blocks /proc/net
```
- **stack=gvisor** (user-space netstack): TCP + UDP both work.
- **stack=mixed is broken on Android 10 MIUI**: gvisor TCP half fails to
  start — DNS/UDP works, TCP SYN hits tun0 (RX counter grows) but engine
  never processes it (no `started at tun0` log) → connection timeout
  (YouTube etc.). UDP DNS works because mixed's system half is fine.
- **auto_detect_interface:false** + unconditional protect() in
  common/dialer/default.go (Android branch) is the working combo; the
  getInterfaces/startDefaultInterfaceMonitor platform impls are harmless
  but not required when auto_detect is off.

## Injection MUST be in Kotlin (covers auto-start)
Flutter-side `_injectAndroidConfig()` only runs on manual start. Auto-start
(MainActivity.onResume → startForegroundService) reads the raw config file,
so force-inject in VpnService.onStartCommand after reading
sing-box-config.json:
- `stack` → gvisor, remove `interface_name`, `auto_detect_interface` → false
- log marker: "Android TUN injected"
Use `org.json.JSONObject` fully-qualified in Kotlin (no import needed).

## VPN stop order (system-bound service)
VpnService is system-bound (BIND_VPN_SERVICE; ConnectivityService holds the
binder). stopSelf()/stopService() alone NEVER trigger onDestroy → stale
fd/tun0 → next establish() registers a VPN network whose traffic never
reaches the engine. Correct order:
1. close TUN ParcelFileDescriptor (removes VPN network → system unbinds)
2. boxService.stop()
3. stopForeground(true) + stopSelf()
Implement as VpnService.shutdown() called from MainActivity stopVpn handler.
Also close the fd in onDestroy/onRevoke.

## Device-side TUN-path debugging (adb)
- `adb shell curl -m 15 https://www.youtube.com` — REAL TUN-path test.
  clash API `/proxies/<tag>/delay` bypasses TUN (engine-direct) — NOT
  representative of app traffic.
- `grep tun0 /proc/net/dev` before/after a `nc` attempt — proves SYN
  physically reached tun0 (RX delta) vs engine never saw it.
- `dumpsys connectivity | grep 'type: VPN'` — VPN network registered?
- `ip rule; ip route show table <N>; ip route get 8.8.8.8` — routing tables
  (fwmark 0xc0000 → table 99; per-uid rules → VPN table; uid 2000 adb shell
  IS routed to tun0).
- `adb shell nc -w 8 8.8.8.8 443` — raw TCP test from shell.
- Writing files into app dir via run-as: use `base64 -d >` with
  MSYS_NO_PATHCONV=1 (MSYS path mangling corrupts adb args).
- UI automation: `uiautomator dump` + grep content-desc for button coords,
  `adb shell input tap x y` to click Start/Stop.

## Diagnosis sequence used (this bug)
1. dumpsys connectivity: VPN network registered but no traffic → app flows
   bypass TUN (default network still wlan0, active default 130).
2. ip rule/table 99 empty + `default dev tun0` in table 1068 → routing
   actually fine for uid 2000 (ip route get confirms).
3. tun0 RX grows on nc (SYN arrives) but singbox.log shows no outbound →
   engine's TUN stack not processing TCP → stack issue (mixed), not routing.
4. `started at tun0` missing in engine log = gvisor half failed → force
   stack=gvisor → fixed.
