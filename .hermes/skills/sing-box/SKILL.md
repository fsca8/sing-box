---
name: sing-box
description: "sing-box backend repo (my_custom fork): builds (dev.sh/rebuild-libbox.sh), UPSTREAM_TAG version system, upstream merges, monitor hooks architecture, Android TUN. Entry point — see references."
version: 2.0.0
created_by: agent
platforms: [windows, android]
tags: [sing-box, netbird, go, gomobile, libbox, monitor]
---

# sing-box（后端仓库，my_custom 分支）

sing-box + Netbird 统一 Go 后端。**最终工作分支：`my_custom`**（sing-netbird 已合入，netbird 集成在 `experimental/netbird_integration/`，`with_netbird` build tag 控制）。集成代码 import `github.com/netbirdio/netbird/client/embed`（**非 internal**——internal 包跨模块不可 import，gomobile/embed 均不可用）。netbird 仓库独立维护：工作分支 `my_custom`（引擎侧补丁）/ `my_custom_server`（服务器端），上游基准 tag 记在仓库根 `UPSTREAM_TAG`（现 v0.76.3），构建经 go.work 直接编译 my_custom 工作树（不再 checkout 上游 tag）。

## 构建

**Windows 统一二进制**（sing-box + netbird）：
```bash
./dev.sh netbird release          # → sing-box-netbird-v<UPSTREAM_TAG>-<hash>-<date>.exe
```
- `UPSTREAM_VERSION` 默认从仓库根 `UPSTREAM_TAG` 文件读取（声明式基准，合并上游时更新）；`tr -d '[:space:]'` 去 CRLF（单引号内换行的 tr 会漏删 \r）
- 版本注入 ldflags：`constant.Version` + `version.version`（netbird 仓库 UPSTREAM_TAG）+ **`main.netbirdBuildCommit` 用 `main.` 前缀**（完整 import path 在 link 不匹配）；libbox 非 main 包用完整路径
- `NetbirdCommit()` 读 main 模块 BuildInfo 只能拿到 sing-box commit——netbird 真实 commit 必须 -X 注入

**Android libbox.aar**（flutter 仓库脚本）：
```bash
bash ~/works/sing-box-flutter/.hermes/skills/sing-box-flutter/scripts/rebuild-libbox.sh
```
- `-target android/arm64 -androidapi 23` + `with_netbird` tag；**必须 go.work 开启**（netbird 仅 go.work 引用）
- 版本注入同 dev.sh（libbox.netbirdBuildCommit 完整路径）
- 去掉 `-buildvcs=false`（VCS revision 嵌入 → SingBoxCommit/BuildTime 可用）

## 版本体系（如实原则）

- **sing-box 版本** = `UPSTREAM_TAG` 文件（如 `v1.14.0-beta.4`）——声明我们基于哪个上游 tag 修改，**不用 git describe**
- **netbird 版本** = netbird 仓库根 `UPSTREAM_TAG` 文件（如 `v0.76.3`，dev.sh/rebuild-libbox.sh 读取，回退 exact-match→development）；**禁止拼 `-hash` 后缀**：version.version 是线上协议字段（客户端上报服务端），带 prerelease 后缀会被服务端 `shouldSkipSendingDeprecatedRemotePeers` 的 `>= 0.29.3` 约束判为老客户端
- `sing-box version` 的 **BuildTime 行 = 嵌入的 vcs.time（commit 时间，非构建时刻）**——`-buildvcs=false` 去掉后 VCS revision 嵌入才有此值
- 展示：`sing-box version` 命令输出 `Netbird: v0.76.0 (hash)` 行（with_netbird tag 分支文件 cmd_version_netbird.go / stub）

## 上游合并流程（v1.14.0-beta.4 实测）

```
git fetch upstream && git merge <tag>          # my_custom 上
```
冲突策略：**monitor hooks/libbox 扩展/netbird 保留 ours**；**上游重构采用 theirs**（dns/router.go、adapter/outbound.go、trafficcontrol 等）；go.mod/go.sum 用 theirs + `go mod tidy`；tracker.go 需在上游 uuid.ID 基础上加回 `ConnID` 字段 + DialMeta 提取（monitor 依赖）。合并后更新 UPSTREAM_TAG。

**protobuf 命名冲突**（netbird v0.76.0 + sing-box v1.14+）：两包都注册 `daemon.LogLevel` → init panic。已改 **sing-box 侧** `daemon/*.proto` → `package sbox.daemon` + protoc 重生成（netbird 不改，保持纯净）。

## Monitor 架构（详见 references/monitor-architecture.md）

- `experimental/monitor/`：Collector（NewCollector 设 globalCollector）+ SQLite（modernc.org/sqlite）
- 记录 hooks：`common/dialer/default.go`（RecordTCP，需 DialMeta）、`common/tls/client.go`（RecordTLS）、`dns/client.go`（RecordDNS）；`route/route.go` 路由前 `ContextWithDialMeta` 注入
- **RecordTCP 不 push connBuf**（ringbuf）→ libbox `GetConnectionHistory` 必须查 `QueryConnections`（SQLite），DNS 同理 `QueryDNS`
- `common/trafficcontrol/tracker.go`：上游 uuid.ID 基础上保留 `ConnID string` 字段（RoutedConnection 从 DialMeta 提取）

## References

- `references/android-tun-debugging.md` — Android TUN 全套（stack=gvisor、Kotlin 注入、TUN fd、MonitorService、adb）
- `references/build-libbox-aar.md` — AAR 编译细节
- `references/monitor-architecture.md` — monitor 架构
- `references/debug-timing.md` / `debugging_patches.md` / `err_connection_closed.md` — 调试
- `references/netcheck_script.md` + `scripts/netcheck.sh` — 服务器诊断
- `references/split-routing-geoip.md` — 分流配置
- `references/configuration-guide.md` — 通用 sing-box 配置（DNS/性能/协议/DoH/广告拦截/TUN vs SOCKS5）

## 关键坑速查

| 坑 | 解法 |
|------|------|
| protobuf daemon 冲突 | sing-box proto → `sbox.daemon` + protoc 重生成 |
| main 包 -X 注入 | `main.` 前缀（完整路径不匹配）；非 main 包用完整路径 |
| UPSTREAM_TAG CRLF | `tr -d '[:space:]'`（单引号内换行的 tr 漏删 \r） |
| gradle vfs.watch 慢 | flutter 侧 gradle.properties `org.gradle.vfs.watch=false` |
| netbird 依赖 | 只在 go.work use 引用（go.mod 无 require）——GOWORK=off 编译不过 |
| route53 依赖冲突 | go.work `replace github.com/libdns/route53 v1.5.0 => v1.6.2` |
| 用户凭证 | setup key/JWT 一律 [REDACTED]，不写日志/输出 |
