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
            echo ""
            echo "Environment:"
            echo "  TAGS      Build tags (default: with_utls,with_gvisor,with_quic,with_wireguard,with_clash_api)"
            echo "  NB        Set to 1 to enable netbird (alternative to 'netbird' arg)"
            echo "  GOTOOLCHAIN Go toolchain version (default: auto)"
            echo "  GOOS      Target OS (default: current)"
            echo "  GOARCH    Target arch (default: current)"
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
# Version prefix: our declared upstream baseline (UPSTREAM_TAG file),
# overridable via UPSTREAM_VERSION env var.
UPSTREAM_VERSION="${UPSTREAM_VERSION:-$(cat UPSTREAM_TAG 2>/dev/null | tr -d '[:space:]' || echo testing)}"
COMMIT_HASH="$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")"
BUILD_DATE="$(date +%Y%m%d)"
VERSION="${UPSTREAM_VERSION}-${COMMIT_HASH}-${BUILD_DATE}"

# ── Build tags ──────────────────────────────────────────────────────
BASE_TAGS="${TAGS:-with_utls,with_gvisor,with_quic,with_wireguard,with_clash_api}"
if [ "$WITH_NETBIRD" = true ]; then
    TAGS="${BASE_TAGS},with_netbird"
    OUTPUT="sing-box-netbird-${VERSION}.exe"
    echo "→ Netbird build ENABLED"
else
    TAGS="${BASE_TAGS}"
    OUTPUT="sing-box-${VERSION}.exe"
fi

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
CGO_ENABLED=1 \
GOOS="${GOOS:-windows}" \
GOARCH="${GOARCH:-amd64}" \
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
