# AGENTS.md

本仓库是 sing-box 的 **fork**（上游：`SagerNet/sing-box`），在上游稳定 tag 之上叠加
netbird 集成等自有功能。给 agent / 新协作者的最小须知，细节见 `docs/` 与 `DESIGN.md`。

## 基线

* `UPSTREAM_TAG` 记录本分支对齐的上游稳定 tag（**唯一权威基线**）。
  upstream `testing` 是会被 force-push 的开发分支，不要用它做基线，也不要 `git describe`
  推断基线（旧基线被上游 rebase 过，tag 关系不可靠）。
* 自有提交作者固定为 fork 作者身份；上游提交保持原样。移植/回移请按主题分提交，
  不要用一次性大 commit 覆盖上游历史。

## 构建

* 普通构建（不含 netbird）：

  ```sh
  go build ./...
  ```

* **netbird 集成（`-tags with_netbird`）必须用 Go workspace**：这些 import 不在
  go.mod/go.sum 里（上游没有该依赖，且本 fork 需要 netbird 侧修改）。见
  `go.work.example`（复制为 `go.work`，按 checkout 位置改 `use` 路径）。缺 workspace 时
  构建会报 `no required module provides package github.com/netbirdio/netbird/...`。
* 工作区 `go` 行取两个模块 requirement 的较大者：netbird 0.80.0/0.81.x 要求 `go >= 1.26.0`
  （其 go.mod 还把工具链钉在 go1.26.7），本仓库是 1.25.5 → `go.work` 写 `go 1.26.0`。
  `dev.sh` 在 netbird 构建时固定 `GOTOOLCHAIN`（默认 go1.26.8）到该线以上。
* ⚠️ **不要跑 `go mod tidy`**：它把 netbird 当远端模块解析，会拉到无法解析的伪版本
  （`pion/ice/v4@v4.0.0-00010101000000-000000000000: invalid version`）。加依赖用
  `go mod edit -require=` + `GOWORK=off go mod download <mod>@<ver>` 落 go.sum。
* Go ≥ 1.27 链接 `experimental/libbox`、`experimental/boxdd` 时会报
  `invalid reference to runtime/pprof.parseProcSelfMaps`（linkname 检查收紧，上游
  v1.14.3 同样复现，与 fork 改动无关）。加 `-ldflags=all=-checklinkname=0`
  （上游 `release/LDFLAGS` 已含此项）；`cmd/sing-box` 桌面产物不受影响。
* `dev.sh` 是构建/部署入口脚本，已把上述 tag、LDFLAGS、workspace 检查固化进去。

## 约定

* **仓库里不许出现本机信息**：路径、用户名、域名、IP、端口、服务名、主机名、工具链位置，
  包括代码注释、脚本默认值、测试夹具、提交信息。需要本机值就让调用方用环境变量传入并在
  缺失时明确报错（本机那套值写在机器级文档里，不在本仓库）。示例网段/域名用文档保留段
  （`192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`、`example.com`）。
* `daemon/*.proto` 的 protobuf 包名是 `sbox.daemon`（不是上游的 `daemon`）：netbird 的
  `client/proto` 也注册了全名 `daemon.LogLevel`，同名 enum 会触发 protobuf 全局注册
  panic。改 `.proto` 后必须重新生成 `*.pb.go` / `*_grpc.pb.go` 并提交生成物。
* 文档分工：`AGENTS.md`（本文，精简、写"该怎么做"）、`README.md`（项目概况）、
  `DESIGN.md`（方案设计）、`BUGS.md`（问题定位与修复记录）。
* 提交前跑机器级泄漏/产物自检脚本（在本机文档仓库的 `_machine/` 下），红了不要提交。
