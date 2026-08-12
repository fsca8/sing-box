---
name: connection-error-triage
description: "连接历史大量 error 时的归因排查(客户端 vs 服务端): monitor.db 按 dest_ip+port 画像对比错误率、服务器侧 ss/ufw/tcpdump 验证、SYN 代理假象识别、adb backup 提取非 debug 包私有数据。"
version: 1.0.0
created_by: agent
platforms: [windows, android, linux]
tags: [singbird, monitor, adb, tcpdump, rustdesk, debugging]
---

# 连接错误排查(客户端 vs 服务端归因)

触发场景: singbird 连接历史/监控出现大量 error 记录, 需要判断是客户端(引擎/VPN/配置)问题还是远端服务问题。典型: 单目标端口高频重试错误堆满历史页。

## 1. monitor.db 错误画像(第一步, 先看错误是否集中)

monitor.db 的 connection_records 表有 `error` 列(空=成功, 非空=失败原因)。提取/查询:

- 非 debug 包拿不到文件时: `adb backup -f app.ab -noapk <pkg>`(设备弹框确认)→ 文件头 4 行文本(ANDROID BACKUP/版本/压缩/加密), 跳过 header 后 `zlib.decompress` → tar → 解出 `apps/<pkg>/f/monitor.db`。**binary 必须走文件方式, 勿用 adb shell cat 文本模式**
- 错误画像 SQL:
  `SELECT dest_ip, dest_port, COUNT(*), SUM(CASE WHEN error IS NOT NULL AND error!='' THEN 1 ELSE 0 END) FROM connection_records GROUP BY dest_ip, dest_port ORDER BY 3 DESC`
- **同 IP 不同端口错误率对比是关键判据**: 同一目标 IP 下某端口全错、其他端口全通(或代理服务器全通)→ 问题在该端口对应服务, 与客户端网络/singbird 无关。代理本身健康度看代理服务器 dest_port(如 12399)的错误率=0
- 时间跨度: 查 first/last, 错误集中在一段时间后自动恢复 = 服务端临时故障/重启窗口, 客户端高频重试(20-30s 一次)只是累积计数

## 2. 服务器侧验证(不能只信公网端口扫描!)

**坑: PC 上测端口 OPEN 是假象** —— 云安全组/本地代理的 SYN 代理会代答 SYN-ACK, `python socket.connect()` 显示 OPEN 但数据实际不通(服务器 tcpdump 看不到 SYN-ACK、本地 connect 127.0.0.1 refused)。

判定必须看服务器本地证据, 顺序:
1. `sudo ss -tulnp | grep <port>`: 监听真相(含 UDP, 如 RustDesk 21116 是 TCP+UDP)
2. `sudo ufw status numbered | grep <port>` + INPUT policy: ufw DROP 且未放行 → SYN 静默丢弃 → 客户端表现为 **i/o timeout(不是 refused)**; 无监听 → RST → refused
3. `docker logs <container> --tail`: 服务自报 Listening 端口(如 hbbs 日志明确列出)
4. **tcpdump 抓包铁证**:
   `sudo timeout 12 tcpdump -i any -nn 'tcp port A or tcp port B' -c 20`
   - 端口 A SYN 进→SYN-ACK 出→握手完成 = 正常
   - 端口 B SYN 进→无响应→SYN 重传 = 无监听或 INPUT DROP
   - 0 packets = 流量根本没到这台机器(云层面被转发到别处)

## 3. 案例: RustDesk 21114 持续 i/o timeout(2026-08-10)

- 现象: 58 条 `dial tcp <ACLOUD>:21114: i/o timeout`, 20-30s 规律重试, ~22 分钟后自动停止; 同 IP 21116 62 次全通
- 归因: 同 IP 同出站, 单端口挂 → 服务端问题; 代理 163 次 0 错误 → 排除 singbird
- 服务器验证: 标准 rustdesk/rustdesk-server hbbs/hbbr 只监听 21115-21119, **21114 是 API 服务器端口(OSS 版不部署)**; ufw 只放行 21115-21119; tcpdump 21114 SYN 进无 SYN-ACK
- 根因: 客户端配置 ID 服务器带了 :21114 端口(或新版客户端 API 探测)→ 连不存在的端口 → 超时重试。**核心功能(注册/穿透/中继)实际正常**
- 修复: 客户端 ID 服务器只填域名不带端口(默认 21116); 或补部署 API 组件并 ufw 放行 21114; 或忽略

## 4. 其他实用坑(本案例踩过)

- **adb 设备不枚举**: WPD 里能看到设备但 adb 无 = ADB 接口未暴露。小米 VID_2717: PID FF40=MTP / FF48=ADB; 只见 FF40 = USB 调试没开或需重枚举(通知栏 仅充电→MTP 切一次 + 授权弹窗)。Get-PnpDevice 里 Code 45 的 ADB Interface 是幽灵设备(历史残留), 不代表在线
- **release 包 run-as 被拒** → 装 debug 覆盖(release 也用 debug key 签名, 可覆盖不丢数据; versionCode 用 `--build-number` 抬过已装值, 否则 INSTALL_FAILED_VERSION_DOWNGRADE; 已装更高时 `adb install -r -d` 降级)
- **adb pull 不认 MSYS /c/ 路径**: 目标目录写 `C:/...` 原生路径, 否则报 "N files pulled" 但实际没落盘
- adb 全路径: `%LOCALAPPDATA%\Android\Sdk\platform-tools\adb.exe`(PATH 里没有)
