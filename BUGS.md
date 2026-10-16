# BUGS.md

问题定位与修复记录。一条一记：**先结论、再证据、后修复**，标题带编号方便 `grep` 定位，
不需要全文件加载。

约定（与 `AGENTS.md` 一致）：本文只写**代码事实**与**可复现现象**，示例一律用文档保留段
（`192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`、`example.com`）；本机的具体
地址/域名/路径不写在这里。涉及具体部署值的复现步骤用 `<overlay DNS 地址>` 这类占位符。

### 状态总览（先看这里）

| 编号 | 一句话 | 状态 | 修复提交 |
| --- | --- | --- | --- |
| BUG-0001 | 内核 TUN 下本机自查 CustomZone 域名永久挂起 | ✅ 已修复 | `944b45300` |
| BUG-0002 | `netbird-status` 空指针 panic（SIGSEGV） | ✅ 已修复 | `944b45300` |
| BUG-0003 | overlay 路由/规则永远装不上（周期自愈被同步检查短路） | ✅ 已修复 | `944b45300` |
| BUG-0004 | 每个失败拨号都打一条 ERROR：日志被拨号失败刷满 | ✅ 已修复 | `96d8e1b5f` `527c79e6f` |
| BUG-0005 | monitor 连接记录只记"打开"半边、字段恒为初值，且每分钟裁剪刷日志 | 🟡 已定位、未修复 | — |

**状态只有三种取值**：`✅ 已修复` / `🟡 已定位、未修复` / `🔴 已知、未定位`。
每条标题下的第一项 "状态" 写了修复提交与验证到什么程度；`grep -n "^## BUG-" BUGS.md`
只列条目，配合本表即可判断"这个 bug 现在是什么情况"。

> ⚠️ **验证基线**：上面三条"已修复"是在**基于 netbird 0.76.3 的 release 构建**上验证的
> （`v1.14.3-a5b7013d6-20261015`；该二进制 `version` 打印 `Netbird: v0.76.3 (b999e306d)`）。
> 之后仓库的 netbird 依赖先升到 0.80.0、再升到 **0.81.1**（v0.80.0→v0.81.1 之间有 100+ 个
> 上游提交）。换到新基线后要重新复核两条最贴上游的路径：BUG-0001 的"隧道 DNS 地址从哪来"、
> BUG-0003 的"overlay 网段从哪来" —— 两者都吃 netbird 侧下发的数据结构。
>
> BUG-0004 是**纯 sing-box 侧**改动（日志出口折叠），不依赖 netbird 版本：它已用**新产物在
> 本机做了一次端到端演示**（本地 SOCKS 入口 + `direct` 出站 + 40 次必然失败的连接，不需要 root、
> 不碰线上服务），但**尚未上机**——下一次 release 构建会同时带上它与 netbird 0.81.1，
> 届时按上面两条重新复核。

---

## BUG-0001 内核 TUN 模式下，本机自查 CustomZone 域名永久挂起：tunnel DNS 地址为空导致从不拨号，fallback 又递归自噬

* **状态**：✅ 已修复（提交 `944b45300`）。已在 kernel-TUN 部署上验证：宿主服务启动 3.5s 内
  装好路由，zone 内名字毫秒级返回，进程连续运行 20 小时以上无重启、无回归。
* **组件**：`experimental/netbird_integration`（`dns_transport.go`、`kernel_route_linux.go`、`netbird.go`）。
* **影响面**：**只影响同时担任隧道 DNS server 的那台 peer 自己**。其余 peer 拿管理端下发的
  nameserver 配置、经隧道查询该 peer 的 DNS server，路径正常。宿主上任何按 CustomZone 域名
  解析的动作（脚本、`curl`、语言运行时）会**永久挂起**——不是慢，是永远不会有答案。

### 结论

内核模式（kernel TUN / Route A）下，tunnel DNS 地址**只在启动那一刻**被写入一次
（`SetupKernelRoute` 里的 `SetDNSAddr`，也是该模式下唯一的写入点）。这一次写入有两种静默失败：

1. 启动时 `WaitSyncResponse` 5s 超时 → 同步结果里没有 overlay 网段 → `ParsePrefix` 报错 →
   **路由/规则都没装**，`NetworkCIDR` 为空还让 `cmd_run_all.go` 的调用直接跳过；
2. 即使拿到网段，`netlink.AddrList` 拿不到 wt0 的 IPv4（netbird 还没把地址配上）→
   原代码在 `for` 循环里**什么都不做、不报错**。

两种情况下 `dnsAddr` 都停在 `""`，而且**此后永远不会再被修**：`postStartSync`（引擎重启后）
在内核模式下**故意跳过**地址设置，周期性刷新也只重写 rule-set 文件。于是
`resolveViaNetbird` 在它第一行就返回错误（**不建任何 socket**），CustomZone 查询
落到公共 DNS fallback；而 fallback 包从本机发出后又被 sing-box 自己的 TUN 劫持
（`auto_route` + 配置里的 `protocol dns → hijack-dns`），再次匹配 `nb-domains` 规则集、
**重新进入同一个 transport**——没有基例的递归，包永远出不去、客户端永远等不到应答。

### 现象（实测）

| 查询 | 结果 |
| --- | --- |
| `nb-domains` 规则集里列出的域（含根本不存在的名字） | 挂起，60s 无应答 |
| 其他域，含**不存在的公共域** | 0.25s 返回 NXDOMAIN |
| 同一个名字，直接用 DNS 客户端问 netbird 监听地址 | 毫秒级、应答正确 |

补充事实：

* `nb-engine.log` 里**一行 DNS 相关的都没有**（只有 ICE/TURN/relay 噪音）。
* state 文件里 `dns_state: null` **不是故障**：本集成不走 netbird 的 host DNS manager，
  而是往 sing-box 配置里注入 DNS server + DNS 规则（见下）。

### 证据

| # | 观测 | 说明 |
| --- | --- | --- |
| E1 | 查询挂起期间按 100ms × 30 采样 `/proc/net/udp`，指向 `<overlay DNS 地址>` 的 socket **恒为 1 个**（只有 listener） | **从未建立过连接 socket**，即从未拨号。原候选 A/B 都要求"包已经发出去了"，因此都不成立 |
| E2 | 同一采样里有数千个 socket：本机 TUN 源地址 → 公共 DNS:53，状态已建立、收发队列均为 0、无超时 | fallback 是唯一"活着"的路径，且每个包都留一个不回收的 socket（递归的直接证据） |
| E3 | 监控库里 `transport='nb'` 的记录**全部** `status=ERROR`、延迟硬卡 10,000ms 量级、`answers` 全空 | 10s 是**调用方**的超时，不是 transport 自己的耗时；该 transport 在整个观测窗口内**一次都没成功过** |
| E4 | 进程 PID 与启动时间连续多日未变；46MB 日志窗口内**一行 netbird 都没有** | 启动期那条 `log.Warn("netbird kernel route: ", err)` 早已滚出窗口 ⇒ `dnsAddr` 自进程启动起就是空 |
| E5 | `ip rule` 里 priority 2000 的规则存在，但它指向的表**是空的** | 表空 + 规则在 = 上一代进程留下的残留；正是"启动期路由没装成"的指纹 |
| E6 | 代码路径核查：`SetDNSAddr` 的调用点中，内核模式下可用的**只有** `kernel_route_linux.go` 的 `SetupKernelRoute`（另两处包在 `!IsKernelMode()` 里，`Shutdown` 只清空） | 该模式下地址没有任何自愈路径 |

### 已排除的候选

* **候选 A（自环进隧道）**：不成立。E1 证明连包都没发出去，谈不上被 overlay 路由吞掉。
* **候选 B（回包错配）**：不成立。同上：没有 socket、没有包，不存在回包。

### 修复

1. **地址改为按查询实时读取**：`RefreshKernelDNSAddr()` 读 wt0 的 IPv4（带 3 次/200ms 重试，
   覆盖"接口还没起来"），内核模式下不再依赖启动快照；地址确实拿不到时**返回错误而不是静默**。
2. **路由/规则改为幂等 reconcile**：`ReconcileKernelRoute()` 重装路由与规则，挂在
   `postStartSync`（引擎每次重启后，wt0 是重建的）与 `network_monitor.go` 的周期 tick
   （5s，见 BUG-0003——原先挂在 30s 的域名刷新里，那条路径在同步失败时会被短路）。
   写入前**先探测现状**（`RouteListFiltered` /
   `RuleList`），避免每 30s 无谓地刷内核路由表、惊动 netbird 自己的网络监视器；
   `RuleAdd` 容忍 `EEXIST`。CIDR 为空时复用上次成功的网段（一次同步抖动不该拆掉可用路由），
   管理端改网段时删掉旧规则。注意 `netlink.NewRule()` 的哨兵值必须保留，零值字面量会写出
   错误的属性。
3. **清理路径可重建**：cleanup 用网段重新构造删除请求，不再复用安装时的对象——wt0 会被重建，
   旧的 link index 会让删除静默失效。
4. **独立硬超时 + 可诊断日志**：`resolveViaNetbird` 加 3s 硬超时（不依赖 `ctx.Deadline()`），
   失败 WARN 带域名、目标地址、耗时、错误，并按 30s 节流 + 汇总抑制（实测失败查询可达
   24 次/秒，不能刷爆日志）。
5. **删除公共 DNS fallback**：CustomZone 名字只可能由隧道 DNS 回答，失败改为 **SERVFAIL**
   ——既不再产生公共 NXDOMAIN 负缓存（把"组网私有域"答成"不存在"），也不再因为
   `hijack-dns` 把这些包收回本 transport 而无限递归。这一条是安全的：该 server 只被
   `nb-domains` 规则选中，注入配置**从不**把它设为 `final`。
6. **数据路径标志每次启动都重新推导**：`Stop()` 会清掉"是否内核模式"的全局标志，而网络切换 /
   引擎恢复走的是 `Engine.Start()`（不经过 `StartAll`），于是**第一次重启之后进程就静默退化成
   用户态 netstack**：DNS 地址按错的路径重算、内核路由自愈也不再触发，日志里毫无迹象。
   现在 `Start()` 用引擎自己的配置重新推导（与 `StartAll` 同一表达式），每次启动都置位。

### 复现（不含具体部署值）

1. 以 kernel-TUN 模式跑一个 netbird 网关，管理端下发一个 CustomZone。
2. 让启动期的 sync 拿不到网段（或让接口地址晚于快照就绪），使 `dnsAddr` 为空。
3. `dig <zone 内的名字>`（经该主机的解析器）→ 挂起；
   `dig <zone 内的名字> @<该主机 overlay DNS 地址>` → 立即应答。

### 修好后怎么验

* `getent hosts <zone 内的名字>` / `dig <zone 内的名字>` 毫秒级返回**隧道的 overlay 地址**；
  zone 内**未登记**的名字也毫秒级返回，不再挂起。
  ⚠️ 但**不要**把"未登记的名字返回 NXDOMAIN"当成判据：netbird 下发的 CustomZone 是**通配域**，
  它下面的任何名字都会答成同一个 overlay 地址（实测未登记名字 `NOERROR` + 该地址，1ms）。
  判据是"**快**"（<1s，而不是 10s/60s 挂起），不是"答 NXDOMAIN"。
* `ip route show table <内核路由表号>` 非空，且 `ip rule` 里对应 priority 的规则指向该表。
* 宿主服务重启后（wt0 会被重建）**立刻**可解析，不需要等下一次引擎重启。
* 引擎因网络变化自动重启后（**不**重启服务）自愈：规则与路由恢复、地址重新读取、解析继续可用。
* 隧道正常但地址暂时取不到时，日志里有 WARN（不再静默），客户端拿到 SERVFAIL。
* 监控库里 `transport='nb'` 的记录不再全是 `ERROR` / 10s。

---

## BUG-0002 `netbird-status` 空指针 panic（SIGSEGV）

* **状态**：✅ 已修复（提交 `944b45300`）。验证：`netbird-status` 退出码 0、打印 `netbird: stopped`、
  不再 SIGSEGV。
* **组件**：`cmd/sing-box/cmd_netbird.go`、`experimental/netbird_integration/netbird.go`。
* **现象**：任何一次 `netbird-status` 都会立即崩溃（SIGSEGV），拿不到任何状态输出。
* **根因**：包级变量 `nbEngine` 只在 `runNetbird()` 里赋值，而 `commandNetbirdStatus`
  直接调用 `nbEngine.GetStatus()`。`Engine` 是**进程内**对象（没有 IPC、没有状态文件），
  所以独立进程里 `nbEngine` 必然是 nil；`GetStatus()` 又是普通方法、没做 nil 接收者保护，
  于是 nil 解引用。
* **修复**：
  * `commandNetbirdStatus` 先判空，输出 `netbird: stopped`（首行与原输出保持一致，便于解析），
    第二行说明"本命令只报告本进程启动的引擎"；
  * `Engine.GetStatus` / `IsRunning` / `GetClient` 加 nil 接收者保护：nil 一律按"没在跑/没有
    client"上报，而不是 panic。
* **注意**：`netbird-status` 报的是**本进程**的引擎状态，跨进程不可见。要看这台机器上的
  隧道/路由实况，用 BUG-0001「修好后怎么验」里的两条 `ip` 命令。
* **验证**：`<二进制> netbird-status` 退出码 0、打印 stopped、不再 SIGSEGV。

---

## BUG-0003 内核模式下 overlay 路由/规则永远不会装上：网段只信管理面同步响应，周期自愈又被同步检查短路

* **状态**：✅ 已修复（提交 `944b45300`）。验证：启动期（t=3.5s）与 5s 周期 tick 两条路径都能把
  规则 + 路由装上，日志里带 `prefix source` 便于取证。
* **组件**：`experimental/netbird_integration/kernel_route_linux.go`、`network_monitor.go`、`netbird.go`。
* **影响面**：kernel-TUN（Route A）部署。后果是**整个 overlay 出口不可用**：本机到其他 peer
  （含 overlay 上的 ssh）被自己的代理 TUN 收走 → 走代理 → 不该出网的 overlay 流量出网且不通。
  更糟的是连带效应：外部监护脚本正是按"专用表里有没有 overlay 网段"判定这台主机是否健康，
  连续判异常后会**执行救援**——把数据面整个拆掉（删内核 TUN / 删代理 TUN / 清规则与表），
  比原始故障影响更大。

### 结论

`SetupKernelRoute(networkCIDR)` 的网段只有一个来源：管理面同步响应里的字段
（`ExtractNetworkCIDR`）。实测部署里那个字段**取不到**——注意**同步本身是成功的**
（同一个响应里的 CustomZone 域名正常拿到了并写进了规则集），只是没有可用的 v4 网段。
于是启动期只留下一条 WARN，规则与路由都没装。

设计上原本有"周期自愈"兜底，但它挂在 `refreshDomainsFromSync()` 里、**在同步检查之后**：

```go
resp, err := WaitSyncResponse(...)
if err != nil || resp == nil { return }   // ← 自愈在这里被短路
...
if IsKernelMode() { ReconcileKernelRoute(ExtractNetworkCIDR(resp)) }
```

即"同步失败"这个**最需要自愈**的场景，恰好是自愈被跳过的场景；进程整个生命周期内都不会
再尝试装路由。

### 现象（实测）

| 观测 | 结果 |
| --- | --- |
| `ip rule` | 有 `priority <优先级> lookup <专用表>` 规则 |
| `ip route show table <专用表>` | **空** |
| 目的地在 overlay 网段内的探测地址 | `via <物理网关> dev <物理网卡>`（完全不经过内核 TUN） |
| 外部监护脚本日志 | 连续 3 轮 `overlay=0 missing: rule:lookup <专用表> table:<专用表>/<overlay 网段>` → 重启 ×2 → `进入救援模式` |

### 证据

| # | 观测 | 说明 |
| --- | --- | --- |
| E1 | 每个进程只在启动约 3s 时打一条 `invalid or missing overlay CIDR ""`，此后 30s 周期里**一条 reconcile 相关日志都没有** | 周期路径从未到达 reconcile（对比：用户态下同一函数确实会打"rule-sets refreshed"） |
| E2 | `ip rule` 有规则、专用表为空，且该规则在多次服务重启后依然存在 | 规则的属性里没有 `dev`，只有路由是 link 绑定的：接口一销毁，路由消失而规则留下——"半装"状态的指纹 |
| E3 | 同步响应确实可用（CustomZone 域名写进了 `nb-domains` 规则集），但 `NetworkCIDR` 为空 | 不是"同步失败"，而是该响应里没有可用网段；只怪同步会误判方向 |
| E4 | 监护脚本三轮判定的间隔约 30s，与自愈周期同量级 | 若自愈真的在跑，不可能连续三轮观察到同一缺失 |

### 修复

1. **网段增加"接口自身"这一来源**（`overlayPrefixFromInterface()`）：读内核 TUN 的 IPv4 地址
   + 掩码并掩码成网段（`<地址>/16` → `<网络>/16`）。接口是 netbird 自己配的，与管理面相
   无关，**只要数据面是活的就一定能拿到网段**；`/32` 或无掩码的地址会被拒绝（那描述的是
   peer，不是网络）。
2. `resolveOverlayPrefix()` 明确优先级：**同步响应 → 接口 → 上次成功安装的网段**，三者都
   拿不到才报错，且错误里带上两个来源各自的失败原因。冷启动绝不凭空猜网段。
3. **自愈搬到网络监视器的 5s tick**（`network_monitor.go`），与同步结果**彻底解耦**；
   `refreshDomainsFromSync()` 回归"只刷域名/网段规则集文件"。失败告警按"消息指纹 + 60s"
   节流，避免接口尚未就绪时刷屏。
4. `ensureKernelRoute` 返回"是否真的写过"，只在**实际安装/修复**时打一条 INFO（含网段与
   来源），既便于事后取证，又不产生周期噪音。
5. `parseOverlayPrefix` 统一返回**已掩码**的网段（路由与规则描述的是网络，不是 peer）。

### 复现（不含具体部署值）

1. 以 kernel-TUN 模式启动，让同步响应的网段字段为空（或让接口地址晚于启动快照就绪）。
2. 启动期会看到一条 `invalid or missing overlay CIDR ""`；此后 30s 一轮，专用表始终为空。
3. `ip route get <overlay 网段内的地址>` → 走物理网卡；overlay 上的 ssh / 服务不通。

### 修好后怎么验

* journal 里出现一条 `netbird kernel route: overlay <网络>/16 → <内核接口> installed
  (prefix source: interface)`。
* `ip route show table <专用表>` 里有指向内核 TUN 的网段路由，且与该接口的 `proto kernel`
  路由网段一致（示例：`<网络>/16 dev <内核接口>`）。
* 目的地在 overlay 网段内的探测地址（取同 /24 的非本机地址，例如 `.253`）：
  `ip route get` 显示 `dev <内核接口>`。**不要**拿本机自己的 overlay 地址当探针——那会命中
  `local` 表，永远显示 `dev lo`。
* 重启宿主服务（内核 TUN 会被重建）后 **5s 内**恢复；外部监护不再报 `overlay=0`。

---

## BUG-0004 每个失败拨号都打一条 ERROR：日志文件被拨号失败刷满

* **状态**：✅ 已修复（提交 `96d8e1b5f`，收口提交 `527c79e6f`）。
  **单测 + 本机端到端演示已过；线上仍未部署**——改动只碰 sing-box 侧的日志出口，
  与 netbird 版本无关，但当前线上跑的二进制里没有这段代码。
* **组件**：`route/conn.go`（失败路径：`E.Cause(...)` + `m.logger.ErrorContext(ctx, err)`）。
* **现象**：跑着 BitTorrent 客户端（`aria2c` 之类）的透明网关上，日志文件**全部内容都是失败拨号**——
  实测某次统计：文件 221,948 行，其中 221,946 行（≈100%）是
  `ERROR [<连接号> <耗时>] connection: open connection to <ip>:<端口> using outbound/direct[direct]:
  dial tcp <ip>:<端口>: i/o timeout | connect: connection refused`。
  实测写入速率 ≈ **4.9 MB / 49 分钟 ≈ 143 MB/天**（按行算约 12 万行/小时）；`log.max_size_mb`
  到顶后文件会被**就地截断**（机制见"修复"第 6 条；观测到一次 52.4 MB → 39.4 MB，目录里没有 `<日志名>.1` 之类的
  轮转文件），所以"文件多大"会严重低估真实写入量。
* **根因**：连接建立失败这条路径**无条件按 ERROR 记一行**，没有去重、没有节流、没有汇总。
  BT 客户端向 DHT / 对等端发起的连接本来大量不可达（超时 / 拒绝），于是**失败率直接等于日志量**。
  这不是"日志级别配错了"：这些确实是 error 级事件，`log.level` 挡不住。
* **为什么不能靠配置绕开**：失败拨号是 BT 的正常行为；改成别的出站或封端口会改变业务语义；
  调短拨号超时只会让单位时间内的失败次数**更多**。

### 修复

1. **折叠挂在日志出口，不改调用点**：新增 `log/fold.go`，用包装 `io.Writer` 的 `foldingWriter`
   套在工厂选定的 sink（文件 / stderr / journald）前面（`defaultFactory.Start()` 里接线，
   `Close()` 时 flush）。理由：失败拨号的 `ErrorContext` 调用点散落在大量上游文件里，
   逐个改既会在每次同步上游时冲突，也只能覆盖一个来源；出口只有一处。
2. **按"形状"计预算**：形状 = 该行抹掉易变部分之后的文本——时间戳前缀、连接号与耗时、
   IPv4/IPv6 地址、`:端口`、长十六进制串、其余数字。窗口内同一形状**先原样输出
   `repeat_limit` 条**（默认 10 条），其余只计数；窗口滚动（或进程退出）时补一行
   `[log-fold] suppressed N of M similar lines in <窗口>; shape: <归一化后的那一行>`。
   抹掉的只是"每次都不一样、但语义相同"的部分：**错误文本、级别、出站标签不同就是不同形状**，
   不会把不同问题并成一行。
3. **宁可多写、不丢日志**：形状表上限 4096 条，满了之后新形状直接原样输出（退化成旧行为）；
   `Write` 恒返回 `len(p)`，不向调用方制造短写错误。
4. **可配置**：`log.repeat_limit`（默认 10，负值关闭）、`log.repeat_window_sec`（默认 300 秒，
   负值关闭）。`NewDefaultFactory` 的折叠参数是**尾部可选参数**，上游调用点一行都不用改。
5. **需要"一条不漏"的用途仍走 monitor 的事件回调或库表**，不要让日志承担计数职责（这条不变）。
6. **顺带把"就地截断"的机制写清楚**：文件到顶后不是外部轮转，而是 `log/rotate.go` 的
   `rotatingFileWriter` 循环覆盖——写满后保留尾部 `maxSize*3/4`，所以观测到的是"文件突然变小"
   而不是出现 `.1` 轮转文件。折叠压总量、循环覆盖保住文件上限，两者互补。
7. **多行负载不折叠、汇总行不留空行**（收口提交 `527c79e6f`，端到端演示暴露的两处）：
   形状键先裁掉行尾换行，否则汇总行自己会再补一个换行、每条汇总后面多一个空行；
   `Write` 发现本次负载是多行（panic 栈、结构化 dump）就**原样透传、不计数**——按行折叠
   这类日志会把栈里各条错误之间的公共行当成"重复行"吃掉，而它们的每一行都有信息量。
   多行负载之前仍然先把过期的折叠计数交代清楚，保证汇总不会跑到它后面。

### 修好后怎么验

* 单测：`go test -race -count=1 ./log/...`，覆盖归一化（时间戳/连接号/地址/端口/数字折叠、
  不同错误不并）、窗口内不超限、汇总行计数正确、形状表满后退化、并发写、
  多行负载逐行透传、汇总行不许多出空行，以及"折叠 + 真实文件 sink"的端到端用例。
* **本机端到端演示**（不需要 root、不碰线上服务）：拿 release 产物起一份最小配置
  （`log.repeat_limit: 3`、本地 SOCKS 入口、`direct` 出站、日志写临时文件），
  对关闭端口打 40 次必然失败的连接，得到 **3 行原样 + 1 行** `[log-fold] suppressed 37 of 40
  similar lines in 1m0s; shape: ERROR [<...> <n>ms] connection: open connection to <ip>:<port>
  using outbound/direct[direct]: dial tcp <ip>:<port>: connect: connection refused`，
  文件 724 B。同时确认：写文件的日志里没有 ANSI 颜色码；`log.repeat_limit` /
  `log.repeat_window_sec` 两个新键能被 `check` 接受。
* **上机后**：`grep -c '\[log-fold\]' <日志文件>` 应能按窗口看到汇总行；
  同一形状不应再超过 `repeat_limit` 行。
* 预期：拨号失败风暴下，同一形状每窗口最多 `repeat_limit` 行 + 1 行汇总
  （原先实测约 12 万行/小时）。

---

## BUG-0005 monitor 连接记录只记了"打开"半边：`dest_port` 恒为 0、字节与关闭状态永不落库，还每分钟裁剪刷日志（已定位、未修复）

* **状态**：🟡 已定位、未修复（噪声治理项）。
* **组件**：`experimental/monitor`（`collector.go`、`database.go`、`maintenance.go`），涉及
  `route/conn.go`、`experimental/clashapi/connections.go` 的接线。
* **现象**（实测某网关上 10 万行 `connection_records`）：

  | 观测 | 结果 |
  | --- | --- |
  | `dest_port = 0` | **全部**行 |
  | `upload_bytes + download_bytes = 0` | **全部**行 |
  | `closed = 1` | **0** 行（`end_time_ms` / `duration_ms` 同理） |
  | 其中本机 BT 客户端的失败拨号 | 99.5%（其余是别的本机进程） |
  | journal 里的 `[monitor] pruned ... records` | **每 60 秒一条**（因为行数一直顶在上限） |

* **根因**：三个互相独立的缺陷叠在一起：
  1. **`dest_port` 没有任何写入点**。表结构、`INSERT`、`SELECT` 里都有这一列，但全仓库没有一处
     给 `ConnectionRecord.DestPort` 赋值；端口其实躺在 `dest_ip` 里（值形如 `192.0.2.1:51413`），
     从来没被拆出来。
  2. **"关闭"与"流量"两条路都没接通**：`Collector.CloseConnection` / `CloseConnByDest` 在全仓库
     **没有调用者**（后者的注释写着 "called from route/conn.go"，但那里并没有调用它）；
     `SyncTraffic` 只在启用了 `clash_api` 的部署里才会被驱动（`clashapi/connections.go`）。
     于是真实部署上 `closed` / 字节数 / 结束时间**永远是初值**。
  3. **裁剪噪音**：行数上限（默认 DNS 20 万 / 连接 10 万）被"失败且零流量"这类无信息量的行顶满后，
     维护任务每分钟删一批，并往 stdout 打一条 `[monitor] pruned ...`（进 journal）。
* **影响**：连接表几乎**不能用来排查真实连接**（没端口、没流量、没时长、没关闭状态）；
  库长期贴着上限，每分钟一次 DELETE + 自愈式收缩；journal 被这条固定噪音占住（实测一天 684 条）。
* **修法方向**（未实施，按性价比排序）：
  1. **接通关闭链路**（正本清源）：在连接结束处调用 `CloseConnection(connID, duration, up, down)`，
     或让 `CloseConnByDest` 真正被调用；两条路都要求 key 与创建时一致（`ConnID`，退化到 `dest_ip`）。
  2. **拆出 `dest_port`**：写入时从目的地址解析端口，`dest_ip` 只放 IP（避免一列两种含义）。
  3. **不给"失败且零流量"的连接建行**：这类事件改成按「出站 + 错误类别」计数，需要明细时靠事件回调；
     否则一台 BT 客户端就能把上限顶满。
  4. **裁剪日志降噪**：`pruned` 只在实际发生显著收缩（或按小时汇总）时打一条，而不是每次跑都打。
* **验证方式**：正常跑一段时间后 `select count(*) from connection_records where dest_port = 0`
  应为 0 或只占极少数；`closed = 1` 且字节数非零的行应占多数；journal 里 `pruned` 明显减少；
  库大小稳定、不再每分钟抖动。
