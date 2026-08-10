---
name: netbird-netstack-inbound-bridge
description: 给 nbembed userspace-netstack 模式的 netbird peer(Android singbird/Windows)暴露本地服务(SSH/Web)的完整方案——诊断签名、ListenTCP 验证、bridge 实施与配置。netstack 与内核栈隔离导致入站数据无处投递,这是唯一不动 netbird 源码的解法。
---

# netbird netstack 入站桥接(expose_ports)

## 触发场景
对端(Windows/其他 peer)SSH 或访问某 Android(singbird)/Windows(nbembed netstack 模式)设备的 overlay IP 失败,而局域网 IP 可访问。

## 根因(架构铁律)
nbembed **userspace netstack**(进程内 TCP/IP 栈,无内核 TUN)与 Android/Linux 内核协议栈**隔离**:
- 隧道入站包 → netstack 用户态栈 → 完成 TCP 握手 → **无应用在 netstack 里监听 → 数据丢弃**
- Termux sshd / web 服务监听的是**内核栈**,永远收不到
- 内核 TUN 模式(官方客户端 wt0)无此问题 → homesfy/dedirock 可被入站访问,Android singbird/Windows singbird 不能

## 诊断签名(快速判定)
| 现象 | 含义 |
|------|------|
| SSH: `Connection established` → `banner exchange timeout` | netstack 完成握手,数据无投递(典型) |
| SSH: 立即 `connect refused` | netstack 回 RST(无监听者时的另一种表现) |
| ping overlay: **0ms + TTL=128** + tracert 第一跳即目标 | **本地栈回显,根本没走隧道**(Windows 特征) |
| mipad→Windows overlay ping 通(2ms) | 隧道本身双向 OK,问题只在目标端入站 |
| homesfy(内核TUN)→mipad overlay:8022 也 refused | 与发起端无关,是 mipad netstack 入站缺失 |

## 方案 A:bridge(实测可行,零改 netbird)
netbird embed **已有** `client.ListenTCP(:port)`——在 netstack 里建标准 TCP 监听,**对端 peer 入站能被 Accept**(2026-08-10 实测:mipad → probe:28022 收到 ACCEPTED + HELLO banner)。

### 实施(改动全在 sing-box 仓库,已落地)
1. `experimental/netbird_integration/bridge.go`:`BridgeTCP(port, target)` = `client.ListenTCP(":port")` + acceptLoop + 每连接 `io.Copy` 双向转发到 `127.0.0.1:target`;`StopBridge`/`StopAllBridges` 幂等
2. `Config.ExposePorts []ExposePortConfig{Port, Target}`,`StartAll` 引擎启动+sync 后遍历 `BridgeTCP`(netbird.go)
3. `libbox/netbird.go` 导出 `NetbirdBridgeTCP(port, target) string` / `NetbirdStopBridge(port) string`(错误转 string;panic 防护用独立 `func ... (retErr error){defer recoverError(&retErr)}` 包装,recoverError 只收 *error)
4. Flutter:`setNetbirdConfig(exposePorts:)` + `setNetbirdExposePorts()` 写入 netbird-config.json 的 `expose_ports` 数组

### 配置
```json
{"setup_key":"...","management_url":"...","device_name":"singbird-mipad","log_level":"info",
 "expose_ports":[{"port":8022,"target":"127.0.0.1:8022"}]}
```
引擎直读此文件,`ParseConfig` 标准 json 解析自动带出字段。

### 资源成本
每端口 = 1 goroutine(阻塞 Accept)+ 1 用户态 TCP endpoint ≈ 几 KB,空闲零 CPU;动态成本按活动连接(每连接 2 goroutine + 2 buffer),与端口数无关。多服务 = 多端口 = 多条目,不是透明转发。

## 验证程序技巧(独立 probe)
- 独立 module `replace netbird => 本地仓库` 时 `go mod tidy` 被 dex 测试依赖卡死(`-e` 也不够,go.sum 缺条目)
- **最快**:临时放 `netbird/cmd/listen-tcp-probe/main.go` 用 netbird 自身 go.sum 编译,`git clean -fd` 删除,不污染仓库
- 验证要新 peer → 需要 setup key(见 memory:离线构造 SHA256+base64,或 dashboard 建 reusable)

## 陷阱
- **Flutter 启动覆盖手写配置**:`_writeNetbirdConfigFile` 用内存字段重写文件,内存无 expose_ports 时会把文件里的清掉 → 必须「内存空时从现有文件读回保留」
- go vet/编译验证:`GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -tags with_netbird ./experimental/libbox/`;netlink 是 Linux 包,Windows 原生编译必失败属正常
- 透明入站(任意端口自动投递)只有两条路:内核 TUN(Android 与 sing-box 单 VpnService 冲突)或改 gvisor netstack——都不可行;借 sing-box VpnService fd 注入也不行(拿不到原始包+overlay IP 内核不识)
- netbird 官方 Expose 需要 proxy 节点(proxies 表 0 条)+ peer_expose_enabled + 组,自建单容器不可用
