#!/bin/bash
# ============================================================================
# <部署主机> sing-bird 服务替换脚本 — 需 root 执行
#
# 用法:
#   sudo bash replace-singbird.sh             # 挑最新 Linux 产物 → 备份 → 替换 → 重启 → 校验
#   sudo bash replace-singbird.sh --dry-run   # 只挑选/校验/打印计划, 不改动任何文件与服务
#
# 环境变量可覆盖: SRC_DIR / DEPLOY_DIR / SVC
#
# 2026-09-13 事故(203/EXEC, Exec format error)与防护:
#   dev.sh 早期默认 GOOS=windows, 且产物名硬编码 .exe —— 一个真正的 Windows PE
#   被当成"最新产物"装进部署目录, systemd 完全无法 exec 它。
#   本脚本三道防线:
#     ① 只挑 file 判定为 ELF 的产物, 非 ELF 一律跳过并告警(不看后缀名);
#     ② 安装前后各校验一次文件类型, 装完不是 ELF 就中止且不启动服务;
#     ③ 校验 systemd 单元的 ExecStart 路径与本脚本写入路径一致
#        (单元指向 sing-netbird, 别人把文件改名成 sing-bird 就没人发现)。
# ============================================================================
set -euo pipefail

SRC_DIR=${SRC_DIR:-<仓库父目录>/sing-box}
DEPLOY_DIR=${DEPLOY_DIR:-<服务实例目录>}
SVC=${SVC:-sing-bird.service}
TARGET_BIN="${DEPLOY_DIR}/sing-netbird"

DRY=0
case "${1:-}" in
    --dry-run) DRY=1 ;;
esac

say() { echo "[replace] $*"; }

# ── 0. 单元 ExecStart 一致性(名字/路径不一致是本次事故的第二个坑) ──────────
UNIT_FILE=$(systemctl show -p FragmentPath --value "$SVC" 2>/dev/null || true)
if [ -n "${UNIT_FILE:-}" ] && [ -r "$UNIT_FILE" ]; then
    EXPECT_BIN=$(awk -F= '/^ExecStart=/{print $2; exit}' "$UNIT_FILE" | awk '{print $1}')
    if [ "$EXPECT_BIN" != "$TARGET_BIN" ]; then
        echo "错误: 单元 $SVC 的 ExecStart 指向 $EXPECT_BIN" >&2
        echo "      本脚本写入的是 $TARGET_BIN" >&2
        echo "      → 请先统一(改单元或改脚本), 否则装完照样起不来" >&2
        exit 1
    fi
    say "单元检查 OK: ExecStart = $EXPECT_BIN"
else
    say "提示: 读不到单元文件(${UNIT_FILE:-未找到 $SVC}), 跳过 ExecStart 一致性检查 (用 sudo 运行即可检查)"
fi

# ── 1. 挑最新产物: 按文件内容判定, 不看后缀 ────────────────────────────────
NEW_BIN=""
while IFS= read -r f; do
    [ -n "$f" ] || continue
    FT=$(file -b "$f")
    case "$FT" in
        *ELF*) NEW_BIN="$f"; break ;;
        *) say "跳过非 Linux ELF 产物: $f  [$FT]" ;;
    esac
done < <(ls -t "${SRC_DIR}"/sing-box-netbird-* 2>/dev/null || true)

if [ -z "$NEW_BIN" ]; then
    echo "错误: ${SRC_DIR} 下没有可用的 Linux ELF 产物 (sing-box-netbird-*)" >&2
    echo "      请先在本机构建: cd ${SRC_DIR} && bash dev.sh netbird release" >&2
    exit 1
fi
say "选定产物: $NEW_BIN"
say "  类型: $(file -b "$NEW_BIN")"
say "  版本: $("$NEW_BIN" version 2>&1 | head -1 || true)"

# ── 2. 计划 ────────────────────────────────────────────────────────────────
TS=$(date +%m%d-%H%M)
say "计划: 备份 ${TARGET_BIN} -> ${TARGET_BIN}.bak-${TS}"
say "计划: 安装 $NEW_BIN -> ${TARGET_BIN}"
say "计划: systemctl reset-failed/stop/start $SVC  (reset-failed 必需: 单元有 StartLimitIntervalSec=600/Burst=5)"
if [ -f "${DEPLOY_DIR}/sing-bird" ]; then
    say "注意: 存在名字相近的残留文件 ${DEPLOY_DIR}/sing-bird [$(file -b "${DEPLOY_DIR}/sing-bird")]"
    say "      它不在单元引用路径上, 建议确认后删除, 以免再次搞混"
fi
if [ "$DRY" = "1" ]; then
    say "--dry-run: 到此为止, 未改动任何文件或服务"
    exit 0
fi

# ── 3. 备份 → 停服务 → 安装 → 安装后校验 ───────────────────────────────────
if [ -f "$TARGET_BIN" ]; then
    cp -v "$TARGET_BIN" "${TARGET_BIN}.bak-${TS}"
fi
systemctl reset-failed "$SVC" 2>/dev/null || true
systemctl stop "$SVC"
install -m 755 "$NEW_BIN" "$TARGET_BIN"
FTD=$(file -b "$TARGET_BIN")
case "$FTD" in
    *ELF*) say "安装后校验 OK: $FTD" ;;
    *) echo "错误: 安装后 $TARGET_BIN 不是 Linux ELF [$FTD] — 已中止, 未启动服务" >&2; exit 1 ;;
esac
systemctl start "$SVC"

# ── 4. 验证 ────────────────────────────────────────────────────────────────
sleep 2
systemctl --no-pager --full status "$SVC" | head -6 || true
say "=== 运行版本 ==="
"$TARGET_BIN" version 2>&1 | head -3 || true
say "=== 进程 ==="
ps aux | grep "[s]ing-netbird" | head -2 || true
if systemctl is-active --quiet "$SVC"; then
    say "完成: $SVC 已是 active"
else
    echo "!! $SVC 启动后不是 active — 排查: journalctl -xeu $SVC" >&2
    exit 1
fi
