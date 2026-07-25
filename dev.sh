#!/bin/bash
# dev.sh — Build sing-box with custom version injection
#
# Version format: <UPSTREAM_VERSION>-<COMMIT_HASH>-<BUILD_DATE>
#   UPSTREAM_VERSION: currently "testing", update when merging upstream release tags
#   COMMIT_HASH:      short SHA of current git commit
#   BUILD_DATE:       YYYYMMDD
#
# Usage:
#   ./dev.sh                    # debug build (no optimizations)
#   ./dev.sh release            # release build (-s -w stripped)
#   ./dev.sh <any-go-flags>     # pass custom flags
#
# Environment:
#   TAGS    — build tags (default: "with_utls,with_gvisor,with_clash_api")
#   DEBUG   — set to 1 for debug build (default when no args)
#   GOOS    — target OS (default: current)
#   GOARCH  — target arch (default: current)

set -euo pipefail

cd "$(dirname "$0")"

# ── Config ──────────────────────────────────────────────────────────
UPSTREAM_VERSION="${UPSTREAM_VERSION:-testing}"
COMMIT_HASH="$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")"
BUILD_DATE="$(date +%Y%m%d)"
VERSION="${UPSTREAM_VERSION}-${COMMIT_HASH}-${BUILD_DATE}"

# ── Build flags ─────────────────────────────────────────────────────
LD_VERSION="-X 'github.com/sagernet/sing-box/constant.Version=${VERSION}'"
TAGS="${TAGS:-with_utls,with_gvisor,with_clash_api}"

# Determine build mode
MODE="${1:-debug}"
if [ "$MODE" = "release" ]; then
    LDFLAGS_SHARED="${LD_VERSION} -s -w -buildid="
    OUTPUT="sing-box-${VERSION}.exe"
    echo "→ Release build: ${VERSION}"
else
    LDFLAGS_SHARED="${LD_VERSION}"
    OUTPUT="sing-box-${VERSION}.exe"
    echo "→ Debug build: ${VERSION}"
    echo "  (use './dev.sh release' for stripped release build)"
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
