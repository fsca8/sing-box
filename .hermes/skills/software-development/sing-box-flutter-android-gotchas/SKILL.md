---
name: sing-box-flutter-android-gotchas
description: Cross-cutting pitfalls for the sing-box-flutter project — cobra bool flags, libbox with_netbird builds, Android VPN permission dialog arg loss, config sync to singleton controller.
version: 1.0.0
---

# sing-box-flutter Gotchas

## Cobra boolean flags MUST use `=` (not space)

`--enable-netbird false` (space-separated) makes cobra treat `false` as a positional arg; the flag itself (no value) defaults to `true`. Netbird would ALWAYS start regardless of the Flutter toggle.

```dart
// WRONG — netbird always starts
['--enable-netbird', isNetbirdEnabled ? 'true' : 'false']
// RIGHT — single arg with equals
['--enable-netbird=$nbEnabled']
```

Symptom in engine stderr: `failed to login to Management Service ... no peer auth method provided`.

## libbox.aar with netbird requires go.work (NOT GOWORK=off)

`experimental/libbox/netbird.go` (`NetbirdStartAll`) compiles only with the `with_netbird` build tag. sing-box's go.mod has NO netbird require — the module resolves solely via `go.work` (`use ..\netbird` + route53 replace). With `GOWORK=off`:
`go: module github.com/netbirdio/netbird: not a known dependency`.

Build tags: `with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_usbip,with_openvpn,with_openconnect,with_netbird,badlinkname,tfogo_checklinkname0`.

## Android: MethodCall args lost across VPN permission dialog

First `startVpn` shows the VPN permission dialog via `startActivityForResult`. The handler's `MethodCall` is NOT kept; `onActivityResult` used to call `startVpnService(null, result)` → configDir/netbirdEnabled silently dropped → engine starts with no config.

Markers tell the tale: `extra config_dir: null` + `sing-box config from file: null chars`.

Fix: `pendingStartVpnCall = call` before the dialog; restore in `onActivityResult` and pass to `startVpnService(pendingCall, result)`.

## Android netbird enable chain

`AndroidVpnController.startVpn(netbirdEnabled:)` → MethodChannel → MainActivity `intent.putExtra("netbird_enabled", v)` → VpnService reads flag, and only reads `netbird-config.json` when enabled (`nbConfig=null` when off) → `BoxService.start(sbConfig, nbConfig)` skips `NetbirdStartAll` when `netbirdConfig == null`. Old intents without the extra default to enabled.

## Singleton controller goes stale (IndexedStack)

`SingBoxController.instance()` reads `ProfileStore` only on first creation. Dashboard lives in an `IndexedStack` so it never rebuilds after profiles change → Start button stays disabled after adding first config.

Fix pattern:
- controller: `reloadFromStore()` — re-reads `activeSingBox`/`activeNetbird` into `setConfig`/`setNetbirdConfig`
- MainShell: `onDestinationSelected` → when switching back to Dashboard tab call `reloadFromStore()`
- ProfilesPage `_createNew`: after save call `_syncActiveToController()` (new profile may lack `active` flag; `getActive()` falls back to first item)

## adb debugging (Android)

```
export PATH="$PATH:/c/Users/fshch/AppData/Local/Android/Sdk/platform-tools"
adb devices
adb shell run-as io.nekohasekai.sfm.sing_box_flutter ls files/logs/
adb shell run-as io.nekohasekai.sfm.sing_box_flutter cat files/logs/markers.log
```
Markers rotate (tail/head to see boot vs startup phases); `files/logs/go_markers.txt` has Go-side markers; `CrashReport-sing-box.log` in `files/`.
