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
| BUG-0005 | monitor 连接记录只记"打开"半边、字段恒为初值，且每分钟裁剪刷日志 | ✅ 已修复 | `8d6dc2971` |
| BUG-0006 | 日志上限的键名与 app 不一致导致引擎拒启动；netbird 引擎日志无上限且不在统一目录 | ✅ 已修复 | `8b059df0d` |
| BUG-0007 | 用户态 netstack 模式隧道 DNS 地址恒为空：带前缀长度的 peer 地址被当裸 IP 解析 | ✅ 已修复 | `8b059df0d` |
| BUG-0008 | `log.max_size_mb` 是 fork 独有的配置键（上游没有日志轮转）：改为固定 100MB 常量 | ✅ 已修复 | `8b059df0d` |
| BUG-0009 | netbird 配置：app 写的 `expose_ports` 被 run-all 丢弃、`admin_url`/`package_name`/`UnifiedConfig` 是死配置/死代码、只有私钥的配置被当成"无凭据"（重构为分组 JSON 单文件） | ✅ 已修复 | `1937cbfa2` |

> BUG-0006～0009 都已提交，但**尚未构建/上机**（下一次 release 产物会一起带上）。

**状态只有三种取值**：`✅ 已修复` / `🟡 已定位、未修复` / `🔴 已知、未定位`。
每条标题下的第一项 "状态" 写了修复提交与验证到什么程度；`grep -n "^## BUG-" BUGS.md`
只列条目，配合本表即可判断"这个 bug 现在是什么情况"。

> ⚠️ **验证基线**：BUG-0001/0002/0003 三条"已修复"是在**基于 netbird 0.76.3 的 release 构建**上
> 验证的（`v1.14.3-a5b7013d6-20261015`；该二进制 `version` 打印 `Netbird: v0.76.3 (b999e306d)`）。
> 之后仓库的 netbird 依赖先升到 0.80.0、再升到 **0.81.1**（v0.80.0→v0.81.1 之间有 100+ 个
> 上游提交）。换到新基线后要重新复核两条最贴上游的路径：BUG-0001 的"隧道 DNS 地址从哪来"、
> BUG-0003 的"overlay 网段从哪来" —— 两者都吃 netbird 侧下发的数据结构。
>
> BUG-0004 与 BUG-0005 都是**纯 sing-box 侧**改动（前者日志出口折叠，后者 monitor 的记账与降噪），
> 与 netbird 版本无关。BUG-0004 已用**新产物在本机做了一次端到端演示**（本地 SOCKS 入口 +
> `direct` 出站 + 40 次必然失败的连接，不需要 root、不碰线上服务）；BUG-0005 有单测覆盖，并配了
> 只读的核对脚本。两者**都尚未上机**——下一次 release 构建会同时带上它们与 netbird 0.81.1，
> 届时按上面两条重新复核，并确认新产物上 BUG-0004 的折叠与 BUG-0005 的字段写入确实生效。
>
> **复核结果（0.81.1）：两条都吃到了下发结构的变化 —— 见 BUG-0007。** 管理端下发的
> `peer_config.address` 带上了前缀长度（`"<ip>/<bits>"`），而旧解析只认裸 IP，
> 于是 overlay 网段恒为空、用户态模式下隧道 DNS 地址跟着恒为空。BUG-0001 那条是在
> 0.76.3 的**内核模式**上验证的，而内核模式的 DNS 地址走接口地址另一条路，
> 正好把用户态这条路径的问题遮住了。

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

## BUG-0005 monitor 连接记录只记"打开"半边：`dest_port` 恒为 0、字节与关闭状态永不落库，还每分钟裁剪刷日志（已修复）

* **状态**：✅ 已修复（提交 `8d6dc2971`）。单测覆盖；上机核对脚本已就位，**尚未在线上产物上复核**。
* **后续变更**（本提交）：monitor 改为两个内存环形缓冲（DNS 2000 / 连接 1000），不再写数据库。
  于是本节里与落库绑定的部分失效——行数上限、体积上限、`retention_days`、每分钟裁剪日志都已
  随库一起删除（`max_size_mb` / `retention_days` / `maintenance_log_sec` 保留为"接受但忽略"，
  否则带这些键的旧配置会因严格解码启动失败），"字节与关闭状态永不落库"换成"永不进环"，
  但本节的修复本身（失败合并、字段补全、失败即关闭记录）继续有效。`dropped` 的语义从
  "被上限裁掉的行"变成"被环覆盖的记录"；历史改为会话级，进程重启即清空，旧 `monitor.db`
  在首次启动时删除。
* **组件**：`experimental/monitor`（`collector.go`、`database.go`、`maintenance.go`、`limits.go`），
  接线在 `cmd/sing-box/cmd_run.go`、`option/experimental.go`。
* **现象**（实测某网关上库贴着上限时的统计）：

  | 观测 | 结果 |
  | --- | --- |
  | `connection_records` 行数 | 100,662（上限 100,000，每分钟都在删旧补新） |
  | `dest_port = 0` | **全部**行 |
  | `dest_ip` 里带 `:端口` | **全部**行（端口一直混在地址列里） |
  | `closed = 1` / `end_time_ms` 非空 | **0** 行 |
  | `upload_bytes + download_bytes > 0` | **0** 行 |
  | `error` 非空（失败行） | 79,273（78.8%） |
  | 写入速率 | 约 731 行/分钟 ≈ 4.4 万行/小时 |
  | `dns_records` 行数 | 200,001（上限 200,000） |
  | 其中重复的失败行 | **197,201**：都是 `transport=nb` + `status=ERROR`、`qtype=A`、无应答、`latency_us ≈ 10,001,000`，集中在 2.3 小时内（约 24 行/秒），而且只是 4 个域名各约 4.9 万次重复 |
  | 重复最多的目的地 | 一个固定的代理出口（占连接表 19.8%） |
  | journal 里 `[monitor] pruned ...` | 每 60 秒一条（100 分钟 120 条，一天数百条） |

  这一批数据本身也是 BUG-0001 的余波：那 197,201 条 DNS 失败全部落在同一次
  "隧道 DNS 地址为空 → 退回公共 DNS → 又被自己的 TUN 劫持"的故障窗口里
  （单条 `latency_us ≈ 10,001,000`，即 10 秒挂起）。故障修掉后这类行不再新增，
  但已经写进库的重复行与"每分钟裁剪"的噪音仍在。

* **根因**：四个互相独立的缺陷叠在一起：
  1. **`dest_port` 没有任何写入点**。表结构、`INSERT`、`SELECT` 里都有这一列，
     但全仓库没有一处给 `ConnectionRecord.DestPort` 赋值；端口其实躺在 `dest_ip` 里
     （值形如 `192.0.2.1:51413`），从来没被拆出来。
  2. **"关闭"与"流量"两条路都没接通**：`Collector.CloseConnection` / `CloseConnByDest`
     在全仓库**没有调用者**（后者的注释写着 "called from route/conn.go"，但那里并没有调用它）；
     字节数只有 `SyncTraffic` 会写，而它只在启用了 `clash_api` 的部署里被驱动。
     于是真实部署上 `closed` / 结束时间 / 时长 / 字节数**永远是初值**。
  3. **失败记录不做合并**：一次上游故障会对同一个目的地反复重试、对同一个域名反复解析，
     每条都建一行。连接表被"失败且零流量"的行顶满（实测 78.8% 是失败行），
     DNS 表更极端（200,001 行里 197,201 行是同一小撮失败的四次方重复）。
  4. **裁剪日志没节流**：行数顶到上限后，维护任务每分钟删一批并往 stdout 打一行（进 journal），
     一天数百行，而且没说清是被行数上限还是体积上限触发的。
* **影响**：连接表几乎**不能用来排查真实连接**（没端口、没流量、没时长、没关闭状态）；
  库长期贴着上限，每分钟一次 DELETE + 自愈式收缩；journal 被这条固定噪音占住。
* **修复**（提交 `8d6dc2971`）：
  1. **拆出 `dest_port`**：新增 `splitHostPort` / `connDest`（基于 `net.SplitHostPort`），写连接行时
     把 `host:port` 拆成 `dest_ip` + `dest_port`；`upsertConnLat` 一并写入端口，冲突分支用
     `COALESCE(NULLIF(excluded.dest_port, 0), connection_records.dest_port)`，
     免得把已经写好的端口覆盖回 0。查询侧本来就 `SELECT dest_port` 并 `Scan(&DestPort)`，
     所以 API 立刻就有真端口，下游不用改。
  2. **失败即"已结束"**：`WriteLatency` 记录到失败（`error` 非空）时直接按结束落库——
     `closed = 1`、`end_time_ms = start_time_ms + max(tcp, tls) / 1000`、`duration_ms` 同值；
     成功的行保持 `closed = 0`、`end_time_ms` 为 NULL，等真正关闭时再补。
     冲突分支里 `closed` 取 `MAX`，时间与时长用 `COALESCE(MAX(a, b), a, b)`——
     ⚠️ SQLite 里 `max(NULL, x)` 是 NULL，裸 `MAX` 会把已有值抹成 NULL。
  3. **失败记录按窗口合并**：`Collector` 增加有界去重表（`failDedupe`，上限 4096 条，FIFO 淘汰），
     连接侧键为 `remote|outbound`，DNS 侧键为 `domain|qtype|status`，
     窗口内重复的失败只让第一条落库。**只抑制数据库写入**：环形缓冲与实时事件推送照旧，
     界面上的实时流水不受影响；成功的连接永远落库。窗口默认 60 秒，`< 0` 关闭。
  4. **裁剪日志节流并讲清原因**：裁剪日志最多每 10 分钟一行，被压掉的轮数与行数累计到下一行里
     （`suppressed N passes, M records`），并写明真正触发的上限（行数上限 / 体积上限）；
     checkpoint 失败仍然每次都报，不参与节流。
  5. **新增两个开关**：`experimental.monitor.fail_dedupe_sec`（默认 60）、
     `maintenance_log_sec`（默认 600）；`0` 表示用默认，**负值表示关闭**
     （前者=关掉去重，后者=不节流）。默认值直接生效，**不需要改配置**；
     `LimitsFrom` 会把 0 回填成默认值，所以只写这两个键不会影响
     `max_size_mb` / `max_dns_rows` / `max_conn_rows` / `retention_days`。
* **没做什么**：没有去接 `CloseConnByDest` 那条断线（那要在上游 `route/conn.go` 的热路径上加调用），
  也没有把 `SyncTraffic` 从 `clash_api` 上解耦——真正的"关闭 + 流量"记账留待另开一项。
* **修好后怎么验**：
  * 单测：`go test -count=1 ./experimental/monitor/...`，覆盖端口拆分、失败即关闭
    （成功的行不许被标记成关闭）、连接与 DNS 两侧的窗口去重与关闭开关、
    裁剪日志节流与被压掉的计数，以及 `LimitsFrom` 的 0 / 负值语义。
  * **上机后**（配套只读脚本，不需要 root）：按服务启动时刻切出新旧两个队列，检查新行里
    `dest_port = 0` 与 `dest_ip` 带端口均为 0、失败行 `closed = 1` 且有结束时间、
    每分钟写入量明显下降、journal 里 `monitor` 行从约 1 条/分钟降到约 1 条/10 分钟、
    DNS 表里同一个 `(domain, qtype)` 的 ERROR 不再成片。
  * 预期：库不再每分钟抖动（体积稳定），连接表里只剩有信息量的行。

---

## BUG-0006 日志文件上限的键名与 app 不一致（严格解码直接拒绝启动）；netbird 引擎日志既不在统一目录、也没有上限

* **状态**：✅ 已修复（提交 `8b059df0d`）。验证到：编译通过（`-tags with_netbird`）、
  `check` 对两种键名的实测（见下）、复用写者的单测本来就覆盖循环覆盖语义。
  **netbird 日志落盘那一段没有端到端验证**（需要真实 netbird 凭据跑起来），
  只到"编译 + 路径/上限由已测写者保证"。组件跨两个仓库：
  sing-box 侧 `option/options.go`、`log/rotate.go`、`experimental/netbird_integration/netbird.go`，
  app 侧（singbird）`lib/services/singbox_controller.dart` 的日志规范化。
* **现象**：
  1. app 生成的配置里写 `log.max_file_size_mb`，而 fork 迁到上游 v1.14.3 后该字段叫 `log.max_size_mb`
     （旧分支用的是 `max_file_size_mb`；改名提交只改了 Go 侧）。sing-box 的配置解码是**严格**的，
     于是 `check -c <app 生成的配置>` 直接：
     `FATAL decode config: log.max_file_size_mb: json: unknown field "max_file_size_mb"`（退出码 1），
     换成 `max_size_mb` 则通过。**只要是带 `log` 段的 profile，引擎根本起不来** ——
     不是"上限没生效"那么轻。
  2. netbird 引擎日志写在 `<DataDir>/nb-engine.log`：不在统一日志目录 `logs/` 下（所以日志中心的
     `netbird` 源恒为空、`LogParser` 里的 logrus 分支永远吃不到东西），裸 `os.OpenFile`
     **没有任何大小上限**，而且 app 的启动清理把它当"旧版散落文件"删掉 —— 跨重启历史全丢。
* **根因**：日志"文件约定"只落在 app 侧文档里，Go 侧的字段名与目录约定各写各的；
  迁移上游版本时字段改名没有任何一处契约检查来兜住。
* **修复**：
  1. `log/rotate.go` 导出 `log.NewFileWriter(ctx, path, maxSize)`，把引擎日志那套循环覆盖写者
     （到顶丢最旧的行、单文件、并发安全）给别的日志复用。
  2. `experimental/netbird_integration/netbird.go`：netbird 引擎日志改到
     `<DataDir>/logs/netbird.log`（与 `engine.log` 同目录），上限 5MB 循环覆盖，目录先 `MkdirAll`。
  3. app 侧：写 `log.max_size_mb`，并**删除**配置里可能残留的旧键 `max_file_size_mb`
     （兼容老版本存下来的 profile 与旧的运行时配置）。
  4. `<DataDir>/nb-engine.log` 的启动清理**保持不变**：现在它只剩"旧版遗留"这一个身份，
     清掉是对的。
* **修好后怎么验**：
  * `check -c`：`log.max_size_mb` 通过；`log.max_file_size_mb` 报 unknown field。
  * 跑起来后 `logs/netbird.log` 会出现且不超过 5MB，`logs/engine.log` 不超过配置的上限，
    两者都**不产生** `.1` 备份文件（循环覆盖语义）。
  * 日志中心的 netbird 源从"恒空"变成有内容；重启 app 后 netbird 历史仍在。
* **没做什么**：没有给 netbird 日志做"备份文件"式的真轮转（那要引入 netbird 自带的
  timberjack 依赖到 sing-box 的 go.mod，或自己实现改名逻辑），统一按循环覆盖处理；
  上限写死 5MB，没做成配置项。

---

## BUG-0007 用户态(netstack)模式下隧道 DNS 地址恒为空：带前缀长度的 peer 地址被当裸 IP 解析

* **状态**：✅ 已修复（提交 `8b059df0d`）。
* **组件**：`experimental/netbird_integration`（`sync_domains.go`、`netbird.go`）。
* **影响面**：**所有走用户态 netstack 的部署**（`kernelTun=false`：Windows app、Android app）。
  netbird 隧道本身是通的 —— 管理端 stream、ICE、WG 握手、P2P/relay、`/netbird/peers`
  全部正常（peer 是 `Connected`，带实时握手与延迟）—— **坏掉的只有 overlay 名字解析**：
  任何 CustomZone 名字（`*.netbird.selfhosted` 与账号里配置的自定义域名）查询都答
  SERVFAIL，于是 app 的"连通性测试"显示"连接失败或超时"，用域名访问 overlay 上的服务
  都在数秒后失败。

### 结论

用户态模式下 tunnel DNS 地址**只有一个来源**：同步响应里的 overlay 网段。

    SetDNSAddr(ComputeDNSAddr(ExtractNetworkCIDR(resp)))      // netbird.go postStartSync

而 `ExtractNetworkCIDR` 的两条来源在新基线上都拿不到值：

1. `NetworkMapEnvelope.full.network.net_cidr` —— 组件式结构，本账号的会话里没有：
   域名走的是 **legacy** `network_map`（`network_map.dns_config` 能取到域名，所以
   `nb-domains.json` 每次启动都被重写，而 `nb-cidr.json` 的 mtime 停在很久以前 ——
   "域名有、网段没有"就是这个 bug 的文件指纹）；
2. `peer_config.address` —— 管理端下发的是 **WireGuard 地址配置 `"<ip>/<bits>"`**
   （服务端用 `fmt.Sprintf("%s/%d", peer.IP, accountNetmask)` 拼出，例如 `100.64.7.9/16`），
   而旧代码只试 `netip.ParseAddr`；`ParseAddr` **拒绝**带前缀的串，于是直接落到 `return ""`。

结果：`ComputeDNSAddr("") == ""` → `dnsAddr` 全程为空 → DNS transport 的
`ensureNetbirdDNSAddr` 每个查询都以 `tunnel DNS address not set` 失败（**不建任何 socket**）。
日志上表现为每个查询一条

    WARN netbird DNS: <name>. via <unset> failed: tunnel DNS address not set (engine has not synced yet)

而同一时刻 netbird 引擎自己的日志显示 `sync finished`、多个 peer `first wg handshake detected`、
relay established —— 极容易误判成"netbird 连不上"。**这句 WARN 的措辞也有误导性**：
"engine has not synced yet" 与实际原因（同步成功、但地址算不出来）无关。

BUGS.md 顶部"换到 0.81.1 后要复核 BUG-0001 的隧道 DNS 地址来源"的警告，兑现的就是这一条。
BUG-0001 当时是在 0.76.3 的**内核模式**上验证的，内核模式的地址由 `SetupKernelRoute`
从 WG 接口地址取（另一条路径），所以用户态这条路径的问题被掩盖了。

### 修复

1. `sync_domains.go`：`ExtractNetworkCIDR` 先用 `netip.ParsePrefix` 解 `"<ip>/<bits>"`
   —— 前缀本身就是 overlay 网段，掩码后直接返回（非 /16 的账户网络也适用）；解不出来
   才退回"裸 IPv4 + 默认 /16"。IPv6、`/33`、非法串仍然返回 `""`。
2. `netbird.go`：`postStartSync`（引擎重启后重同步）与周期刷新 `refreshDomainsFromSync`
   （网络监视器每 30s 一次）都改成**只在算出非空地址时才写** `SetDNSAddr` —— 增量/残缺的
   同步响应不许把已经生效的地址擦掉；算不出来时打一条 WARN。周期刷新这一处同时补上了
   "首次 5s sync 等待超时"的自愈：此前该场景下地址会空到进程结束。

### 验证

* 单测：`TestExtractNetworkCIDR` 新增带前缀（`/16`、`/24`）、IPv6、`/33`、非法串用例；
  新增 `TestPrefixedPeerAddressEndToEnd` 断言 `ComputeDNSAddr(ExtractNetworkCIDR(resp))`
  在带前缀地址的响应上给出可用地址。
  `go test -tags with_netbird ./experimental/netbird_integration/` 全绿。
* 上机（修复前，用户态）：引擎 Clash API `/dns/query?name=<自定义域名>&type=A` 返回
  `Status: 2`（SERVFAIL）且无 Answer，同一时刻 `/netbird/peers` 里 peer 是
  `conn_status: Connected` 并带 `last_handshake`；对照 `/dns/query?name=example.com` 正常。
  这组对照就是"隧道通、DNS 不通"的定位证据。
* 修复后：同一条 `/dns/query` 应返回 Answer（overlay 地址），"连通性测试"用域名应能连上。

### 没做什么

* 没有改成"从 netbird 客户端的接口地址取 DNS 地址"（上游 `client/iface/device` 的做法）——
  那要碰 netbird 的 embed API 与 netstack 生命周期，改动面远大于解析一个前缀；当前
  `sync response → 网段 → last-but-one IP` 的推导与上游 `GetLastIPFromNetwork` 一致
  （已有 `TestComputeDNSAddrMatchesNetbird` 对照）。
* 没动那句 WARN 的 "engine has not synced yet"（它同时也覆盖"引擎还没起来"的真实情形）。

---

## BUG-0008 `log.max_size_mb` 是 fork 独有的配置键：fork 写出的配置上游不认（改为固定 100MB 常量）

* **状态**：✅ 已修复（提交 `8b059df0d`，未构建/未上机）。
* **组件**：`option/options.go`、`log/log.go`、`log/rotate.go`、`log/rotate_test.go`、
  `docs/configuration/log/index.md|index.zh.md`；app 侧 `lib/services/singbox_controller.dart`。

### 结论

日志文件上限**不需要**是配置项。去掉 `log.max_size_mb` 字段，引擎固定使用
`log.DefaultLogFileMaxSize`（100 MiB）的循环覆盖写者；app 侧不再写这个键，并顺手清掉
历史版本可能残留在配置里的 `max_size_mb` / `max_file_size_mb`。

### 证据（为什么必须去掉）

* fork 的 `option.LogOptions` 多了一个字段：

  ```go
  MaxSizeMB int `json:"max_size_mb,omitempty"`
  ```

  上游 `origin/testing` 的 `LogOptions` 只有 `disabled` / `level` / `output` / `timestamp` /
  `disable_color`。上游**完全没有**日志轮转：`log/observable.go` 用
  `filemanager.OpenFile(..., os.O_APPEND|os.O_CREATE|os.O_WRONLY, ...)` 一路追加到底，
  `git grep -in 'rotation\|logrotate\|lumberjack' origin/testing` 零命中。
* sing-box 的配置解码是**严格**的：多一个未知键就是
  `decode config: json: unknown field "..."` + 退出码 1。因此
  **fork 生成的配置拿给上游跑会直接起不来**（反向兼容：上游配置给 fork 跑没问题）。
  这是 BUG-0006 的另一半 —— 那边是键名不一致（`max_file_size_mb` → `max_size_mb`）把**自己**
  弄挂了，这次是键本身**只存在于 fork**。
* app 侧 `_normalizeLogOutput` 过去会往每个 profile 的引擎配置里硬写 `max_size_mb = 5`，
  于是**所有**导出的引擎配置都带一个上游不认的键。

### 修复

* `option/options.go`：删掉 `MaxSizeMB` 字段，留注释说明"上限是引擎常量、不是配置项"。
  （`experimental.monitor.max_size_mb` 是另一个子系统的字段，与日志无关，保持不动。）
* `log/log.go`：不再从配置推导，`log.output` 指向文件时直接用 `DefaultLogFileMaxSize`；
  内部 `fileMaxSize` 参数保留（`NewDefaultFactory` / `NewFileWriter` 的 API 不变，
  netbird 引擎日志的 5MB 上限走同一个 `NewFileWriter`，不受影响）。
* `log/rotate.go`：`DefaultLogFileMaxSize` 的注释改成"常量、故意不可配置"，并说明原因。
* 顺手修的测试卫生：新增 `(*rotatingFileWriter).Close()`（`Write` 加 closed 守卫），
  `rotate_test.go` 7 个用例各加 `t.Cleanup(func() { _ = writer.Close() })`。
  在此之前 Windows 上 `t.TempDir()` 清理删不掉仍被打开的文件，6 个用例报
  `TempDir RemoveAll cleanup: ... being used by another process` —— 断言其实是通过的，
  但 `go test ./log/...` 整体 FAIL，等于这条回归网没有可信信号。
* 文档：`docs/configuration/log/index*.md` 删掉 `max_size_mb` 结构行与字段段，
  改在 `#### output` 下写明"上限固定 100 MB、循环覆盖、不可配置"。
* app 侧：`_normalizeLogOutput` 不再写键，改为**移除** `max_size_mb` 与 `max_file_size_mb`
  两个历史键（只要存在就重写配置）；`README.md`、`lib/logkit/README.md` 同步。
  这一步是必须的，否则新引擎读到旧配置里的 `max_size_mb: 5` 会因未知字段拒绝启动。

### 行为差异（有意为之）

| 配置里的 `log.max_size_mb` | 旧引擎 | 新引擎 |
| --- | --- | --- |
| 缺失 | 100 MB（旧代码 `==0` 分支就是默认值） | 100 MB |
| 正数 | 按该值（app 写的是 5） | 未知字段 → **拒绝启动** |
| 负数 | 关闭上限（无上限） | 未知字段 → **拒绝启动** |

"不配置"的行为完全一致，失去的只是"用负数彻底关掉上限"这一能力 —— 代价是配置格式与上游分叉，
不值得保留。

### 验证

* `go test -count=1 ./log/...` → `ok github.com/sagernet/sing-box/log`（7 个轮转用例全绿）。
* `go vet ./log/... ./option/... ./cmd/sing-box/... ./experimental/libbox/... ./experimental/netbird_integration/... ./experimental/boxdd/...`
  → 只剩两处**既有的** `possible misuse of unsafe.Pointer`（`libbox/debug.go`、
  `boxdd/authenticode_windows.go`，与本次改动无关）；改动涉及的包全部编译通过。
* app 侧：`flutter analyze` → `No issues found!`；`flutter test` → 54 个用例全过。
* **未做**：没有构建、没有部署。上机时**引擎与 app 必须一起换**：新 app + 旧引擎没问题
  （旧引擎把缺失的键当默认 100MB）；**旧 app + 新引擎会挂**（旧 app 会写 `max_size_mb: 5`）。
  另外换上新引擎后要让 app 重启一次，它才会把磁盘上已有的 `sing-box-config.json`
  重写成不带该键的版本。

### 后续变更（2026-10-18）

* app 侧 `_normalizeLogOutput` 的死键清理改成**无条件**：原文只在"输出目标是 `engine.log`"
  这条分支里清键，配置里写成 `stderr`/`stdout`（或 `disabled`）时死键会原样留在磁盘上 ——
  那恰好就是新引擎拒绝启动的情形。现在只要出现 `max_size_mb` / `max_file_size_mb` 就重写配置。
* 同处给**缺失或空的 `log.level` 补 `info`**：引擎缺省是 `trace`（`log/log.go`），
  netbird 侧早已补 `info`，只有引擎这一边没补；这些行在 Windows 上还会被控制器从 stderr
  收进 app.log。完全没有 `log` 段的配置会被补一个最小的 `{"level":"info"}`
  （不动用户没有表达过的输出目标）。
* `log/rotate.go`：轮转保留比例 **3/4 → 1/2**。保留 r 的写放大是 `1/(1-r)`（每写
  `(1-r)*maxSize` 字节就要前移 `r*maxSize` 字节），3/4 ⇒ 4×、1/2 ⇒ 2×。代价是保留窗口
  减半（稳态 50～100 MB），日志是纯诊断数据，换 SSD 写入量减半划算。
  `rotate_test.go` 的 7 个用例不依赖该比例，无需改动。

---

## BUG-0009 netbird 配置：app 写的 `expose_ports` 被丢弃、死配置键与死代码、只有私钥的配置被当成"无凭据"

* **状态**：✅ 已修复（提交 `1937cbfa2`，未构建/未上机）。
* **组件**：`experimental/netbird_integration/`（`config_file.go` 新增、`netbird.go`、
  `netbird_stub.go`、`config_file_test.go` 新增、删 `testdata/unified-example.json`）、
  `cmd/sing-box/cmd_run_all.go`、`cmd/sing-box/cmd_netbird.go`、`test-kernel-tun.sh`、
  `docs/configuration/netbird/index.md|index.zh.md`（新增）、`mkdocs.yml`；app 侧
  `lib/pages/profile_edit_page.dart`、`lib/services/singbox_controller.dart` 等。

### 结论

`netbird-config.json` 过去是**扁平键 + 一把只被部分读取的键**：app 写进去的键，引擎有的读、
有的丢、有的根本不看；反过来引擎支持的一堆选项（MTU、PSK、Rosenpass、批量参数、DNS 标签……）
app 里既没有 UI 也没有写入口。本次把配置文件重构成 sing-box 风格的**分组 JSON 单文件**，
并顺手修掉四个由此暴露出来的缺陷：

1. **`expose_ports` 在 run-all 路径上是死配置**：app 给 Android/Windows 都写了它，
   引擎的 `startBridges(cfg.ExposePorts)` 却永远收到空列表。
2. **`admin_url`、`package_name`、`data_dir` 从来没有人读**：app 一直写 `package_name`。
3. **`UnifiedConfig` / `ReadUnifiedConfig` / `testdata/unified-example.json` 是没人调用的半成品。**
4. **只有 `private_key` 的配置被当成"没有凭据"**，整个 netbird 集成被静默跳过。

### 证据

* `cmd/sing-box/cmd_run_all.go` 的 `buildNetbirdConfig` 从文件里只搬运
  `SetupKey` / `JWTToken` / `ManagementURL` / `DeviceName` / `LogLevel` / `KernelTun` / `PrivateKey`
  七个字段 —— `ExposePorts`（以及 `AdminURL`）被丢掉。于是：
  * app 侧 `settings_page.dart` 为 `Platform.isAndroid || Platform.isWindows` 显示端口转发行，
    `singbox_controller.dart` 把它写进 `expose_ports`；
  * 引擎 `StartAll` → `startBridges(e.cfg.ExposePorts)` 里的 `cfg.ExposePorts` 恒为空；
  * 桌面（Windows）上这条功能**完全无效**，且没有任何报错 —— 配了端口转发却连不进来。
* `package_name`：app 写（Android 专属包名），`git grep -n package_name` 在
  `experimental/netbird_integration/` 里只命中这条写入路径，引擎侧零读取。
* `admin_url`：只在旧 `Config` 结构体里出现过，无任何使用点。
* `UnifiedConfig` / `ReadUnifiedConfig`：`git grep -n 'UnifiedConfig\|ReadUnifiedConfig'` 只有
  定义处与被删掉的 `testdata/unified-example.json`，没有任何调用者。
* `StartAll` 的入场检查是
  `hasCreds := cfg.SetupKey != "" || cfg.JWTToken != ""` —— 只用 `private_key`
  （复用已有设备身份）的配置被判为无凭据，直接 `return result, nil`（sing-box 照常启动、
  netbird 静默缺席）。

### 修复

* 新增 `experimental/netbird_integration/config_file.go`：唯一的配置 schema 与解析入口。
  * 分组：`device`（`name` / `management_url` / `setup_key` / `jwt_token` / `private_key`）、
    `network`（`kernel_tun` / `mtu` / `wireguard_port` / `disable_ipv6` /
    `disable_client_routes` / `block_lan_access` / `block_inbound` / `nat_external_ips` /
    `dns_labels`）、`security`（`pre_shared_key` / `rosenpass` / `rosenpass_permissive`）、
    `lazy_connection`（三态指针）、`expose_ports`、`performance`、`log`。
  * **严格解码**（`json.Decoder.DisallowUnknownFields`，与 sing-box 自身配置一致）：
    未知键、类型错误、非法取值都是错误，`LoadConfigFile` 把它交回调用方。
  * 默认值收敛到 `applyDefaults`：`sing-netbird` / `https://api.netbird.io:443` / `info`；
    MTU 与 WireGuard 端口 `0` 表示"沿用已存/平台默认"，校验范围 `576..8192` / `1..65535`。
  * `hasCredentials()` 把 `private_key` 与环境变量回退（`NB_SETUP_KEY` / `NB_JWT_TOKEN` /
    `NB_PRIVATE_KEY`）一起算进来 —— 修掉上面第 4 条。
  * 旧的扁平格式仍可解析：`legacyFlatKeys` 里的键被抬进分组结构并打一条列出键名的弃用警告，
    分组里的值优先（半迁移的文件行为可预测）。`admin_url` / `package_name` / `data_dir`
    额外各打一条"被忽略"的警告。**旧文件不可能把新引擎卡死在启动失败上。**
    `expose_ports` 不在 legacy 列表里 —— 它同名同形，属于现行 schema，用新格式的文件
    不该为它收到弃用警告。
* `cmd/sing-box/cmd_run_all.go`：改用 `LoadConfigFile`，**整份 Config 原样使用**
  （`ExposePorts` 由此第一次真正到达 `startBridges`）；文件不存在才回落到环境变量；
  文件可读但不可用（未知键/非法值）时打 ERROR 并**禁用集成**（不带着被静默丢掉的选项启动，
  也不因为一个可选子系统让整个代理进程退出）。usage 文本同步成新格式。
* `cmd/sing-box/cmd_netbird.go`：`netbird-run` 同样走 `LoadConfigFile`，并把
  `DataDir` 设为配置文件所在目录（与 run-all 的"数据目录由 sing-box 配置位置推导"同一规则）。
* `netbird_stub.go`（`!with_netbird`）镜像新的分组结构，删掉 `UnifiedConfig`。
* 规则集文件 `nb-domains.json` / `nb-cidr.json` 从数据目录根移进 **`nb-state/`**：
  数据目录根只留用户需要看的配置文件（`sing-box-config.json`、`netbird-config.json`、
  `profiles.json`、`logs/`），`nb-state/` 是引擎内部状态（WireGuard 密钥、peer 状态、
  规则集），换凭据时本来就会被清空 —— 规则集跟着清掉正合期望。
  两个 writer 都补了 `os.MkdirAll(dir, 0700)`：异步启动路径会先写规则集文件、后由 `Start()`
  建目录。
* 文档：新增 `docs/configuration/netbird/index.md` 与 `index.zh.md`（结构 + 字段 + 数据目录 +
  迁移对照 + 命令行），`mkdocs.yml` 里挂到 Configuration 下。

### 行为差异

| 配置 | 旧引擎 | 新引擎 |
| --- | --- | --- |
| 扁平键（`device_name` 等） | 正常 | 正常 + 弃用警告（列出键名） |
| `expose_ports`（桌面） | 被丢弃、功能失效、无提示 | 生效 |
| `admin_url` / `package_name` | 静默忽略 | 警告后忽略 |
| `data_dir` | 生效（可把状态指到别处） | 警告后忽略；数据目录只由 sing-box 配置位置决定 |
| 只有 `private_key` | 判为无凭据、跳过 | 正常启动 |
| 未知键 / 非法值 | 静默忽略（或按零值使用） | 报错并禁用 netbird |
| 规则集文件位置 | 数据目录根 | `<数据目录>/nb-state/` |

### 验证

* `go test -tags with_netbird -count=1 ./experimental/netbird_integration/` → `ok`（新增
  `config_file_test.go`：分组解析、未知键拒绝、类型错误、默认值、扁平格式迁移与警告、
  `expose_ports` 不误报弃用、MTU/端口/URL/端口转发校验、`hasCredentials`（含私钥与环境变量）、
  `LoadConfigFile` 缺文件/坏文件、`nb-state` 路径）。
* `go vet` 两个 tag 变体（`with_netbird` / 无 tag）覆盖
  `./experimental/netbird_integration/ ./cmd/sing-box/ ./experimental/libbox/` → 只剩两处**既有的**
  `possible misuse of unsafe.Pointer`（`libbox/debug.go` 等，与本次改动无关）。
* `go build -tags with_netbird ./...` 通过（无 tag 变体也通过）。
* **未做**：没有构建产物、没有部署。上机时**引擎与 app 必须一起换**：
  * 新引擎 + 旧 app：安全（旧 app 的扁平文件被接受并警告）；
  * **旧 app 也不会变坏，但新 app + 旧引擎会让 netbird 静默不启动** —— 旧引擎把分组键当
    未知字段静默忽略，于是读不到 `setup_key`、判为"无凭据"直接跳过（不报错）。
    这是"必须一起换"的硬理由。

