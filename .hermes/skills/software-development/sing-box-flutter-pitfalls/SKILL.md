---
name: sing-box-flutter-pitfalls
description: sing-box-flutter 项目踩坑记录 — Cobra布尔flag、Hive盒不一致、IndexedStack配置过期、Android netbird开关链路。
version: 1.0.0
---

# sing-box-flutter 踩坑记录

## 1. Cobra BoolVarP 布尔 flag 必须用等号传参

`--enable-netbird false`（空格分隔）会被 Cobra 当作位置参数，`--enable-netbird` 无值时默认 `true` → 开关永远生效为 true。

```dart
// 错误：netbird 永远启动
'--enable-netbird', isNetbirdEnabled ? 'true' : 'false',
// 正确：等号单参数
'--enable-netbird=$nbEnabled',
```

## 2. Hive 读写必须用同一个 box

`saveNetbirdEnable` 写 `_box`、`getNetbirdEnable` 读 `_settingsBox` → 开关保存后读取永远 false。

规则：同一个 key 的 getter/setter 必须操作同一个 Hive box。

## 3. IndexedStack + 单例配置过期

`SingBoxController.instance()` 只在首次调用时从 ProfileStore 读配置；Dashboard 在 IndexedStack 中不重建 → 新增/编辑配置后 `hasConfig` 仍为旧值，启动按钮灰色。

解法：
- controller 提供 `reloadFromStore()`（重读 active 配置）
- MainShell `onDestinationSelected` 切回 Dashboard tab 时调用
- `_createNew` 保存后 `_syncActiveToController()`（新 profile 可能无 active 标记，`getActive()` 回退第一个）

## 4. Android netbird enable 开关链路

Android 启动 VPN 时 Flutter 只传 `configDir` 的话，Kotlin 端只要 `netbird-config.json` 存在就调 `NetbirdStartAll`，开关无效。完整链路必须传：

```
Flutter startVpn(netbirdEnabled) → MethodChannel args['netbirdEnabled']
  → MainActivity call.argument → intent.putExtra("netbird_enabled", ...)
    → VpnService getBooleanExtra → 关闭时 nbConfig=null
      → BoxService.start: netbirdConfig==null 跳过 NetbirdStartAll
```

旧 intent 兼容：无 extra 时默认 true。

## 5. netbird 依赖 go.work（GOWORK 陷阱）

sing-box go.mod **没有** netbird require，只通过 go.work `use (.. ../netbird)` 引用。`GOWORK=off` 时 `go list -m github.com/netbirdio/netbird` 报 "not a known dependency"。

- 纯 sing-box 编译：GOWORK=off 无影响
- with_netbird 编译：必须保留 go.work
