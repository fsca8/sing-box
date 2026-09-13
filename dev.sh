#!/bin/bash
# dev.sh — Build sing-box with custom version injection
#
# Version format: <UPSTREAM_VERSION>-<COMMIT_HASH>-<BUILD_DATE>
#   UPSTREAM_VERSION: currently "testing", update when merging upstream release tags
#   COMMIT_HASH:      short SHA of current git commit
#   BUILD_DATE:       YYYYMMDD
#
# Usage:
#   ./dev.sh                          # debug build (sing-box only)
#   ./dev.sh release                  # release build (-s -w stripped)
#   ./dev.sh netbird                  # debug build (sing-box + netbird)
#   ./dev.sh netbird release          # release build (sing-box + netbird)
#   ./dev.sh <any-go-flags>           # pass custom flags
#
# Environment:
#   TAGS    — build tags (default: "with_utls,with_gvisor,with_clash_api")
#   DEBUG   — set to 1 for debug build (default when no args)
#   GOOS    — target OS (default: current)
#   GOARCH  — target arch (default: current)
#   NB      — set to 1 to enable netbird (alternative to 'netbird' arg)

set -euo pipefail

cd "$(dirname "$0")"

# ── Parse args ──────────────────────────────────────────────────────
WITH_NETBIRD=false
BUILD_MODE="debug"

for arg in "$@"; do
    case "$arg" in
        netbird) WITH_NETBIRD=true ;;
        release) BUILD_MODE="release" ;;
        --netbird) WITH_NETBIRD=true ;;
        help|--help|-h)
            echo "sing-box dev build script"
            echo ""
            echo "Usage: ./dev.sh [options] [netbird] [release]"
            echo ""
            echo "Commands:"
            echo "  help              Show this help"
            echo "  release           Release build (-s -w stripped)"
            echo "  netbird           Include netbird engine (build tag: with_netbird)"
            echo ""
            echo "Examples:"
            echo "  ./dev.sh                          Debug build (sing-box only)"
            echo "  ./dev.sh release                  Release build (sing-box only)"
            echo "  ./dev.sh netbird                  Debug build (sing-box + netbird)"
            echo "  ./dev.sh netbird release          Release build (sing-box + netbird)"
            echo "  GOOS=windows ./dev.sh netbird release   Cross-build Windows (产物带 .exe)"
            echo ""
            echo "Environment:"
            echo "  TAGS      Build tags (default: with_utls,with_gvisor,with_quic,with_wireguard,with_clash_api)"
            echo "  NB        Set to 1 to enable netbird (alternative to 'netbird' arg)"
            echo "  GOTOOLCHAIN Go toolchain version (default: auto)"
            echo "  GOOS      Target OS (default: current host; 只有 windows 产物带 .exe)"
            echo "  GOARCH    Target arch (default: current host)"
            echo "  UPSTREAM_VERSION Version prefix (default: testing)"
            exit 0
            ;;
    esac
done

# Also check env var
if [ "${NB:-0}" = "1" ]; then
    WITH_NETBIRD=true
fi

# ── Config ──────────────────────────────────────────────────────────
UPSTREAM_VERSION="${UPSTREAM_VERSION:-testing}"
COMMIT_HASH="$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")"
BUILD_DATE="$(date +%Y%m%d)"
VERSION="${UPSTREAM_VERSION}-${COMMIT_HASH}-${BUILD_DATE}"

# ── Target platform ─────────────────────────────────────────────────
# 默认跟随当前系统(go env GOOS/GOARCH)。交叉编译需显式指定, 例如
# Windows 客户端: GOOS=windows GOARCH=amd64 ./dev.sh netbird release
# 产物后缀随目标平台: 只有 windows 带 .exe, 本机产物是无后缀的 ELF。
# 判断平台请看文件内容(file), 不要只看后缀 —— 203/EXEC 事故的教训。
TARGET_GOOS="${GOOS:-$(go env GOOS)}"
TARGET_GOARCH="${GOARCH:-$(go env GOARCH)}"
EXT=""
if [ "$TARGET_GOOS" = "windows" ]; then
    EXT=".exe"
fi

# ── Build tags ──────────────────────────────────────────────────────
BASE_TAGS="${TAGS:-with_utls,with_gvisor,with_quic,with_wireguard,with_clash_api}"
if [ "$WITH_NETBIRD" = true ]; then
    TAGS="${BASE_TAGS},with_netbird"
    OUTPUT="sing-box-netbird-${VERSION}${EXT}"
    echo "→ Netbird build ENABLED"
else
    TAGS="${BASE_TAGS}"
    OUTPUT="sing-box-${VERSION}${EXT}"
fi

echo "→ 目标平台: ${TARGET_GOOS}/${TARGET_GOARCH}  (产物: ${OUTPUT})"

# ── Build flags ─────────────────────────────────────────────────────
LD_VERSION="-X 'github.com/sagernet/sing-box/constant.Version=${VERSION}'"

if [ "$BUILD_MODE" = "release" ]; then
    LDFLAGS_SHARED="${LD_VERSION} -s -w -buildid="
    echo "→ Release build: ${VERSION}"
else
    LDFLAGS_SHARED="${LD_VERSION}"
    echo "→ Debug build: ${VERSION}"
    echo "  (use './dev.sh release' for stripped release build)"
fi

# Ensure GOTOOLCHAIN is set for netbird builds (requires go >= 1.25.5)
if [ "$WITH_NETBIRD" = true ] && [ "${GOTOOLCHAIN:-auto}" = "auto" ]; then
    export GOTOOLCHAIN=go1.25.5
    echo "  GOTOOLCHAIN=go1.25.5 (netbird requires go >= 1.25.5)"
fi

# ── Build ────────────────────────────────────────────────────────────
set -x
# 平台由上面的 TARGET_GOOS/TARGET_GOARCH 决定。注意: 注释不要插在
# "VAR=1 \" 续行中间, 那会截断续行, 使 CGO_ENABLED 不再传给子进程。
CGO_ENABLED=1 \
GOOS="$TARGET_GOOS" \
GOARCH="$TARGET_GOARCH" \
  go build \
    -v \
    -trimpath \
    -tags "${TAGS}" \
    -ldflags "${LDFLAGS_SHARED}" \
    -o "${OUTPUT}" \
    ./cmd/sing-box
{ set +x; } 2>/dev/null

echo "→ Done: ${OUTPUT}"
echo "   Version: ${VERSION}"
echo "   Size: $(ls -lh "${OUTPUT}" | awk '{print $5}')"
