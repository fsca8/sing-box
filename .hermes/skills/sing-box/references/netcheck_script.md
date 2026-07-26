# netcheck.sh — 服务器网络诊断脚本

在代理服务器上执行，自动收集带宽、延迟、丢包、实际下载速度等信息。

## 使用方法

```bash
# 复制到服务器并运行
cat > ~/netcheck.sh << 'SCRIPT'
#!/bin/bash
OUTPUT="/tmp/netcheck_$(date +%Y%m%d_%H%M%S).log"
echo "========================================" > "$OUTPUT"
echo "服务器网络诊断报告" >> "$OUTPUT"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S')" >> "$OUTPUT"
echo "主机: $(hostname)" >> "$OUTPUT"
echo "========================================" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== 1. 系统信息 ===" >> "$OUTPUT"
echo "CPU 负载: $(uptime)" >> "$OUTPUT"
free -h >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

echo "=== 2. 网络接口 ===" >> "$OUTPUT"
ip addr show | grep -E "^[0-9]|inet " >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"
echo "带宽测速（下行 10MB）：" >> "$OUTPUT"
curl -s -o /dev/null -w "  速度: %{speed_download} 字节/秒\n" \
  --connect-timeout 15 --max-time 30 \
  https://speed.cloudflare.com/__down?bytes=10485760 >> "$OUTPUT" 2>&1 || echo "  测速失败" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== 3. sing-box 监听端口 ===" >> "$OUTPUT"
ss -tlnp | grep -E "12399|12398" >> "$OUTPUT" 2>&1
echo "  ESTABLISHED 连接数:" >> "$OUTPUT"
ss -tn state established | grep -E "12399|12398" | wc -l >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

echo "=== 4. 延迟测试（ping 10次）===" >> "$OUTPUT"
for target in 8.8.8.8 149.154.167.41 1.1.1.1 223.5.5.5; do
  echo "--- ping $target ---" >> "$OUTPUT"
  ping -c 10 -W 3 $target >> "$OUTPUT" 2>&1
  echo "" >> "$OUTPUT"
done

echo "=== 5. 路由追踪 ===" >> "$OUTPUT"
for target in 8.8.8.8 149.154.167.41; do
  echo "--- traceroute $target ---" >> "$OUTPUT"
  traceroute -n -w 2 $target >> "$OUTPUT" 2>&1
  echo "" >> "$OUTPUT"
done

echo "=== 6. 实际下载测速 ===" >> "$OUTPUT"
for url in "https://www.google.com/generate_204" "https://www.baidu.com" "https://www.cloudflare.com"; do
  echo "--- curl $url ---" >> "$OUTPUT"
  curl -s -o /dev/null -w "  HTTP=%{http_code} TCP=%{time_connect}s TLS=%{time_appconnect}s TOTAL=%{time_total}s SPEED=%{speed_download}B/s\n" \
    --connect-timeout 15 --max-time 30 "$url" >> "$OUTPUT" 2>&1 || echo "  请求失败" >> "$OUTPUT"
  echo "" >> "$OUTPUT"
done

echo "=== 7. 本地回环延迟 ===" >> "$OUTPUT"
ping -c 10 127.0.0.1 >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

echo "=== 8. 网卡统计 ===" >> "$OUTPUT"
ip -s link | head -30 >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"
echo "诊断完成，报告文件: $OUTPUT" >> "$OUTPUT"
SCRIPT

chmod +x ~/netcheck.sh
sudo bash ~/netcheck.sh
```

## 关键指标解读

| 指标 | 正常值 | 异常信号 |
|------|--------|----------|
| 下行带宽 | > 50MB/s | < 5MB/s 说明带宽不足 |
| ping 8.8.8.8 | < 1ms（US机房） | > 50ms 或丢包 |
| ping 223.5.5.5 | < 60ms（US→中国） | > 150ms 或丢包 |
| Google 下载 | < 200ms TOTAL | > 1s 说明服务器到目的地网络差 |
| 网卡丢包 (RX dropped) | 0 或极少量 | > 0.1% 说明上游链路问题 |
