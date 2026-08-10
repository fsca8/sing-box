---
name: netbird-overlay-inbound-bridge
description: "让 netstack 模式(Android/Windows singbird)的 netbird 引擎暴露本地服务——诊断入站不通、验证 ListenTCP、实现 expose_ports 桥接。适用场景为 SSH/Web 服务从其他 netbird peer 访问不到,局域网却通。"
---

# netbird overlay 入站桥接(expose_ports)

## 触发条件

- 从其他 netbird peer SSH/访问某设备 overlay IP 失败,局域网 IP 却正常
- Android(或 Windows)上 singbird 的 netbird 是 nbembed netstack 模式
- 症状: TCP 握手能成(`Connection established`)但数据面超时/拒绝

## 原理(先诊断,别急着写代码)

netbird 在 Android 上是**进程内用户态 netstack**(因为 sing-box 占了唯一 VpnService):
- 隧道包解密后进 Go 进程的 netstack,与内核协议栈**隔离**
- netstack 完成 TCP 握手,但无应用监听 → 数据无投递(banner 超时)或 RST
- 内核里的服务(sshd/web)永远收不到;ping overlay 显示 0ms+TTL=128 = 本地栈回显,**不是**隧道通

对比: 官方客户端内核 TUN 模式(Linux wt0)入站透明;netstack 模式不行。

## 诊断步骤

1. **确认症状分层**:
   ```
   ping overlay IP       → 通但 0ms TTL=128(本地栈,勿当隧道证据)
   TCP overlay:8022      → OPEN(握手被 netstack 完成)
   SSH overlay:8022      → banner 超时 或 Permission denied(有 bridge 后)
   SSH 局域网 IP:8022    → 正常(对照)
   ```
2. **确认隧道双向通**(排除隧道问题):
   ```
   mipad: ping -c 3 <对端overlay IP>   # 应 1-3ms 通
   ```
3. **验证 ListenTCP 前提**(方案可行性,零改 netbird):
   - 临时 Go 程序: nbembed.New(Options{SetupKey, ManagementURL}) → Start → `client.ListenTCP(":28022")` → Accept 循环打印
   - 从对端 peer `curl telnet://<本机overlay>:28022` → 能收到 HELLO banner = 前提成立
   - 注意: 需新 setup key(one-off 用完即废;reusable 可复用); 临时程序放 `netbird/cmd/<name>/` 用其 go.sum 编译, 或独立 module 用 `replace` + `go mod tidy -e`(dex 测试依赖会卡 tidy)

## 实施(改动全在 sing-box 仓库,netbird 零改动)

1. `experimental/netbird_integration/bridge.go`:
   - `BridgeTCP(port, target)` → `client.ListenTCP(":"+port)` + acceptLoop + 每连接 `io.Copy` 双向转发到 target
   - 每端口 = 1 阻塞 goroutine + 1 用户态 endpoint(几 KB,静态零 CPU); 资源按连接数算
   - 幂等: 同 port 重复调用返回已有 bridge; `StopBridge`/`StopAllBridges`
2. `Config` 加 `ExposePorts []ExposePortConfig`(json `expose_ports`, 字段 port/target), stub 同步
3. `StartAll` 引擎启动后遍历 `BridgeTCP`(须在 engine+sync 之后)
4. `libbox/netbird.go` 导出 `NetbirdBridgeTCP`/`NetbirdStopBridge`(gomobile 自动出 Kotlin 方法; recoverError 要 `*error` 包装,不能直接 `*string`)

## 配置

`netbird-config.json` 加字段(引擎启动时自动读取):
```json
{"setup_key":"...","management_url":"...","device_name":"...","log_level":"info",
 "expose_ports":[{"port":8022,"target":"127.0.0.1:8022"}]}
```
- Android 写入: `adb shell run-as <pkg> sh -c 'echo "...json..." > files/netbird-config.json'`(run-as 下 heredoc 会因 /data/local/tmp 无权限失败, 用 echo 转义)
- Flutter 侧 `setNetbirdConfig` 加 exposePorts 参数/`setNetbirdExposePorts` 写入; 日常启动不重写文件, 手动改的字段保留
- 每个服务一条映射; 想暴露 N 个服务就 N 条(不透明但轻量)

## 验证

```
ssh -p 8022 u0_a129@<overlay IP>
# 之前: banner exchange timeout
# 之后: Permission denied (publickey,...) = sshd 已应答, 链路通 ✅
```
logrus 日志不进 singbox.log(输出位置问题), 以功能行为为准。

## 坑

- **监听多个端口**: 静态成本=每端口 1 goroutine+几 KB, 无感; 别按"每端口一个完整代理"想
- **透明入站**(任意端口自动投递)netstack 模式做不到: 需内核 TUN(与 tun0 冲突)或改 gvisor 原始包透传; "借 sing-box VpnService fd 注入"不可行(拿不到原始包 + overlay IP 内核不识)
- 官方 Expose 方案需服务器 proxy 节点(proxies 表 0)+ peer_expose_enabled + 组, 成本高
- DNS zone 按 distribution_groups 分发: 换组后 sync 才生效(server-g 拿不到 client-g 的 zone → 公网 DNS 解析失败)
- setup key 离线构造: 明文=大写 UUID, 库中存 SHA256+base64; 修改 store.db 需 docker cp 出/回 + 重启容器
