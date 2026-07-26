# libbox.aar Build Guide

## Prerequisites

- Android NDK 27.x (not 28.2 — linker crashes on Windows 32-bit ARM)
- JDK 17 or 21 (build script checks for "openjdk 17"; patch to also accept 21)
- SagerNet gomobile fork: `go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.12`
  - Official `golang.org/x/mobile` gomobile does NOT support `-libname` flag

## Environment

```bash
export ANDROID_HOME="C:\\Users\\<user>\\AppData\\Local\\Android\\Sdk"  # Windows native path
export ANDROID_NDK_HOME="${ANDROID_HOME}\\ndk\\27.0.12077973"
unset http_proxy https_proxy  # TUN mode must NOT have proxy env vars
```

## Build

### Official build (via SagerNet script)

```bash
cd ~/works/sing-box
go run ./cmd/internal/build_libbox -target android
# Output: libbox.aar (SDK 23) + libbox-legacy.aar (SDK 21)
```

### Manual arm64-only (faster iteration)

```bash
"$HOME/go/bin/gomobile" bind \
  -v -target android/arm64 -androidapi 23 \
  -o libbox.aar -javapkg io.nekohasekai -libname box \
  -trimpath -buildvcs=false \
  -tags "... ,badlinkname,tfogo_checklinkname0" \
  -ldflags "-s -w" \
  ./experimental/libbox
```

## Required Build Tags

| Tag | Required? | Notes |
|-----|-----------|-------|
| `with_gvisor` | Yes | TUN stack |
| `with_utls` | Yes | VLESS Reality TLS |
| `with_quic` | Yes | QUIC transport |
| `badlinkname` | **Yes** | Prevents `invalid reference to os.checkPidfdOnce` |
| `tfogo_checklinkname0` | **Yes** | Paired with badlinkname |
| `with_wireguard` | Usually | WireGuard support |
| `with_clash_api` | Usually | Monitoring API |
| `with_naive_outbound` | Optional | May fail — needs libcronet.a compatible with NDK |
| `with_tailscale` | Optional | Large dependency (~60 packages, slow compile) |

## Common Failures

### 1. `invalid reference to os.checkPidfdOnce`

**Fix:** Add `badlinkname,tfogo_checklinkname0` to build tags.

### 2. NDK 28.2 linker segfault (Windows)

ld.lld.exe stack trace on armeabi-v7a linking.

**Fix:** Use NDK 27.x. NDK 28.2 has a known `ld.lld` stability issue on Windows.

### 3. `libcronet.a` static library link errors

Happens with `with_naive_outbound` tag. Chronet static libs in Go module cache may be incompatible with NDK version.

**Fix:** Omit `with_naive_outbound` tag unless Naive outbound is actually needed.

### 4. `-trimpath` / `-buildvcs=false` interpreted as linker flags

**Wrong:** `-ldflags "-s -w -trimpath -buildvcs=false"` — these are Go build flags, not linker flags.

**Correct:**
```bash
gomobile bind -trimpath -buildvcs=false -ldflags "-s -w" ...
```

### 5. `gomobile: file exists` on retry

**Fix:** `rm -rf build/` before retrying.

### 6. Android SDK not found (Windows MSYS)

**Cause:** The build script uses `os.ExpandEnv("$ANDROID_HOME")`. MSYS paths like `/c/Users/...` are not recognized by Go's path handling on Windows.

**Fix:** Use Windows-style paths:
```bash
export ANDROID_HOME="C:\\Users\\<user>\\AppData\\Local\\Android\\Sdk"
```

### 7. JDK version check: `java version should be openjdk 17`

The build script does strict string match. Patch `cmd/internal/build_libbox/main.go`:
```go
if !strings.Contains(javaVersion, "openjdk 17") && !strings.Contains(javaVersion, "openjdk 21") {
```

### 8. `flag provided but not defined: -libname`

Official gomobile doesn't support `-libname`. Install SagerNet fork:
```bash
go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.12
go install github.com/sagernet/gomobile/cmd/gobind@v0.1.12
```

## TUN Proxy Env Variable Pitfall

When sing-box is in TUN mode, **never** set `http_proxy`/`https_proxy` env vars.
TUN intercepts all traffic at network level — a local proxy port is not needed and will cause "Connection refused" errors in all CLI tools (curl, Dart pub, git, go install).

**Fix:** `unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY`

## Build Time

- Full build (4 arches + 2 variants): 15-20 min
- arm64 only: 5-8 min
- Cached rebuild: 1-3 min

## Kotlin/Android Integration Gotchas

When wiring `libbox.aar` into an Android project (Flutter or native):

### PlatformInterface implementation
- Every method from `experimental/libbox/platform.go` must be overridden
- **Check Go source**, not decompiled .class — the Go interface is the source of truth
- `gomobile` generates Java interfaces from Go interfaces

### Specific method traps
- `WIFIState()` — no no-arg constructor. Use `Libbox.newWIFIState("", "")`
- `LocalDNSTransport` — Go interface, becomes Java interface (no constructor). Return `null`
- `Libbox.initialize()` — **does not exist**, do not call it
- All Go `error` returns become `throws Exception` in Kotlin
- Return types in Go pointers (`*ConnectionOwner`, `*BridgeSession`) become nullable in Kotlin

### Flutter project integration
- Project must be created with `flutter create` — manually created projects lack Gradle wrapper and are rejected by `flutter build apk`
- AGP 8.x+: remove `package` attribute from `AndroidManifest.xml`, namespace belongs in `build.gradle.kts`
- `local.properties` must have Windows-style paths: `flutter.sdk=C:\\Users\\...\\.puro\\envs\\default\\flutter`

## Windows DLL Build (cgo)

### Setup

```bash
export PATH="/c/msys64/mingw64/bin:$PATH"  # gcc from MSYS2
cd ~/works/sing-box
```

### C export entry point

Create `cmd/monitor-dll/main.go`:
```go
//go:build windows
package main
import "C"
import _ "github.com/sagernet/sing-box/experimental/libbox"
func main() {}
```

C-exported functions go in `experimental/libbox/monitor_ffi.go` (also `//go:build windows`).

### Build

```bash
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  go build -buildmode=c-shared -o libbox.dll \
  ./cmd/monitor-dll/
```

### Verification

```bash
objdump -p libbox.dll | grep "monitor_"   # list exported functions
file libbox.dll                            # PE32+ DLL x86-64
```

### DLL functions return heap C strings via `C.CString`. Caller must free with `monitor_free_string`.

### Callback pattern (Go → C → Dart)

Go callbacks require platform-specific assembly trampolines. **Instead use polling**: Go side appends events to a ring buffer, Dart calls `monitor_poll_events()` periodically and receives a JSON array of new events. Simpler and avoids cgo callback complications.

