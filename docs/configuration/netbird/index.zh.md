# Netbird

Netbird 组网集成，使用 `with_netbird` 构建标签编译进统一二进制。

Netbird 客户端**运行在 sing-box 进程内部**：没有独立的 Netbird 守护进程，不会有第二个客户端去安装内核路由，
也只有一个进程需要托管。组网的自定义域名与 CIDR 会被注入 sing-box 的 DNS 与路由规则。

## 配置

集成使用自己的配置文件，**与 sing-box 配置文件位于同一目录**：

```text
netbird-config.json
```

单独成文是刻意的：切换 sing-box 配置不会动到 Netbird 身份，组网配置也只有一个明确的归属，
而不是藏在 sing-box 配置里。

每次运行时启用：

```sh
sing-box run-all -c /path/to/sing-box-config.json --enable-netbird
# 或指定其它配置文件
sing-box run-all -c /path/to/sing-box-config.json --enable-netbird --netbird-config /path/to/netbird-config.json
```

文件不存在时，凭据取自环境变量（`NB_SETUP_KEY`、`NB_JWT_TOKEN`、`NB_PRIVATE_KEY`、`NB_DEVICE_NAME`、
`NB_MANAGEMENT_URL`、`NB_LOG_LEVEL`）。

完全没有凭据时，集成会被跳过，sing-box 按原样运行。

### 结构

```json
{
  "device": {
    "name": "sing-netbird",
    "management_url": "https://api.netbird.io:443",
    "setup_key": "",
    "jwt_token": "",
    "private_key": ""
  },
  "network": {
    "kernel_tun": false,
    "mtu": 0,
    "wireguard_port": 0,
    "disable_ipv6": false,
    "disable_client_routes": false,
    "block_lan_access": false,
    "block_inbound": false,
    "nat_external_ips": [],
    "dns_labels": []
  },
  "security": {
    "pre_shared_key": "",
    "rosenpass": false,
    "rosenpass_permissive": false
  },
  "lazy_connection": null,
  "expose_ports": [
    {"port": 8022, "target": "127.0.0.1:8022"}
  ],
  "performance": {
    "preallocated_buffers_per_pool": 0,
    "max_batch_size": 0
  },
  "log": {
    "level": "info"
  }
}
```

### 字段

#### device

本节点在网络中的身份与凭据。

* `name`：本节点在网络中的名称，默认 `sing-netbird`。
* `management_url`：管理服务器地址，默认 `https://api.netbird.io:443`。
* 通常只设置一个凭据 —— `setup_key`（可复用或一次性 setup key）、`jwt_token`（管理服务器签发的 JWT）
  或 `private_key`（复用已有设备身份）。留空的凭据会回退到 `NB_SETUP_KEY` / `NB_JWT_TOKEN` /
  `NB_PRIVATE_KEY`。

#### network

数据路径与寻址行为。

* `kernel_tun`：创建 Linux 内核 TUN 设备并为其安装静态路由，替代用户态 netstack。
  需要 root，且只在 Linux 上生效；其它平台始终使用 netstack。
* `mtu`：隧道接口 MTU，取值 `576`..`8192`。`0` 保留已存/默认值（1280）。
* `wireguard_port`：本地 WireGuard 监听端口，取值 `1`..`65535`。`0` 表示随机端口。
* `disable_ipv6`：禁用 IPv6 组网寻址。
* `disable_client_routes`：不安装从管理服务器学到的路由。希望组网流量只由 sing-box 规则引导时使用。
* `block_lan_access`：作为路由节点时，禁止本节点访问宿主机的本地网络（RFC 1918、链路本地、回环）。
* `block_inbound`：拒绝所有来自其它节点的入站连接。
* `nat_external_ips`：为 ICE 候选选择把外部 IP 映射到内部 IP 或网卡，格式为
  `203.0.113.7/32=10.0.0.2/32` 或 `203.0.113.7/32=eth0`。
* `dns_labels`：本节点额外拥有的 DNS 标签。

#### security

* `pre_shared_key`：隧道接口的 WireGuard 预共享密钥。
* `rosenpass`：启用 Rosenpass 后量子密钥交换。
* `rosenpass_permissive`：启用 Rosenpass 的节点仍可连接未运行 Rosenpass 的节点。

#### lazy_connection

Netbird 懒连接的本地覆盖开关。

不设置（`null`）时遵循管理服务器下发的特性开关；`true` / `false` 会双向覆盖它 ——
当对某个节点的首个请求不允许等待按需建连时，设为 `false`。

#### expose_ports

组网 → 本地 TCP 端口转发，每个条目是一个对象：在组网内监听的 `port`，以及转发到的本地 `target`
（`host:port`）。

```json
"expose_ports": [
  {"port": 8022, "target": "127.0.0.1:8022"},
  {"port": 8080, "target": "127.0.0.1:8080"}
]
```

用户态 netstack 能接受组网 TCP 连接，却无法把连接交给监听在宿主机内核协议栈上的进程，
因此每个希望被其它节点访问的本地服务都需要一条配置。端口不可重复，target 必须是 `host:port` 地址。

#### performance

* `preallocated_buffers_per_pool`：限制每条隧道的缓冲区池（`0` 表示不设上限）。
* `max_batch_size`：每次系统调用读写的包数（`0` 使用平台默认值）。

!!! note

    这两个值在 Netbird 的 embed API 中是进程级全局的：在这里设置的值也会成为同一进程中之后创建的
    所有客户端的默认值。

#### log

* `level`：`trace`、`debug`、`info`、`warn` 或 `error`，默认 `info`。

日志目标不可配置：引擎写入其数据目录下的 `logs/netbird.log`，与 sing-box 日志放在一起。

### 数据目录

数据目录不属于配置文件的内容 —— 它由 sing-box 配置文件所在位置推导，这样两个引擎对状态位置的理解始终一致：

```text
<数据目录>/
├── sing-box-config.json
├── netbird-config.json
├── logs/
│   ├── sing-box.log
│   └── netbird.log
└── nb-state/            # 引擎状态，请勿手工编辑
    ├── config.json
    ├── state.json
    ├── nb-domains.json
    └── nb-cidr.json
```

`nb-state/` 存放 WireGuard 密钥、节点状态，以及被注入的 sing-box 配置所引用的规则集文件。
它是引擎内部目录，会按需重新生成。

### 说明

未知字段与非法取值都是错误：引擎会报出问题字段并禁用集成，而不是带着一个被静默丢弃的选项启动。
旧版本的扁平格式配置文件仍会被接受，并给出提示、指出需要迁移的字段 —— `device_name` →
`device.name`、`setup_key` → `device.setup_key`、`jwt_token` → `device.jwt_token`、`management_url` →
`device.management_url`、`private_key` → `device.private_key`、`log_level` → `log.level`、`kernel_tun` →
`network.kernel_tun`。旧的 `admin_url`、`package_name`、`data_dir` 字段引擎从未读取，会被忽略。

## 命令行

`netbird-run` 独立启动集成（用于调试），`netbird-status` 打印其状态：

```sh
sing-box netbird-run --netbird-config /path/to/netbird-config.json
sing-box netbird-status
```
