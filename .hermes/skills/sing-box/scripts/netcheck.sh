#!/bin/bash
# Server-side network diagnostics for sing-box / proxy troubleshooting
# Usage: chmod +x netcheck.sh && sudo bash netcheck.sh
# Output: /tmp/netcheck_<timestamp>.log

OUTPUT="/tmp/netcheck_$(date +%Y%m%d_%H%M%S).log"
echo "========================================" > "$OUTPUT"
echo "Server Network Diagnostics Report" >> "$OUTPUT"
echo "Time: $(date '+%Y-%m-%d %H:%M:%S')" >> "$OUTPUT"
echo "Host: $(hostname)" >> "$OUTPUT"
echo "========================================" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== System Info ===" >> "$OUTPUT"
echo "CPU: $(uptime)" >> "$OUTPUT"
free -h >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

echo "=== Network Interfaces ===" >> "$OUTPUT"
ip addr show | grep -E "^[0-9]|inet " >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

echo "=== Bandwidth Test (10MB download) ===" >> "$OUTPUT"
curl -s -o /dev/null -w "  Speed: %{speed_download} B/s\n" \
  --connect-timeout 15 --max-time 30 \
  https://speed.cloudflare.com/__down?bytes=10485760 >> "$OUTPUT" 2>&1 || echo "  FAILED" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== Proxy Ports ===" >> "$OUTPUT"
ss -tlnp | grep -E "12399|12398" >> "$OUTPUT" 2>&1
echo "  Established: $(ss -tn state established | grep -E '12399|12398' | wc -l)" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== Ping (10 packets) ===" >> "$OUTPUT"
for target in 8.8.8.8 149.154.167.41 1.1.1.1 223.5.5.5; do
  echo "--- ping $target ---" >> "$OUTPUT"
  ping -c 10 -W 3 $target >> "$OUTPUT" 2>&1
  echo "" >> "$OUTPUT"
done

echo "=== HTTP Download ===" >> "$OUTPUT"
for url in "https://www.google.com/generate_204" "https://www.cloudflare.com" "https://www.baidu.com"; do
  echo "--- curl $url ---" >> "$OUTPUT"
  curl -s -o /dev/null -w "  HTTP=%{http_code} TCP=%{time_connect}s TLS=%{time_appconnect}s TOTAL=%{time_total}s SPEED=%{speed_download}B/s\n" \
    --connect-timeout 15 --max-time 30 "$url" >> "$OUTPUT" 2>&1 || echo "  FAILED" >> "$OUTPUT"
  echo "" >> "$OUTPUT"
done

echo "=== Firewall Rate Limits ===" >> "$OUTPUT"
iptables -L -n -v 2>/dev/null | grep -i "limit\|connlimit" >> "$OUTPUT" 2>&1 || echo "  iptables N/A" >> "$OUTPUT"
echo "" >> "$OUTPUT"

echo "=== Interface Stats ===" >> "$OUTPUT"
ip -s link >> "$OUTPUT" 2>&1
echo "" >> "$OUTPUT"

cat "$OUTPUT"
