---
name: sing-box-flutter-installer
description: "singbird Windows EXE 安装包 (Inno Setup): 打包命令、坑、静默安装验证。dev.sh installer 配套知识。"
version: 1.0.0
created_by: agent
platforms: [windows]
tags: [inno-setup, installer, windows, packaging, singbird]
---

# singbird Windows 安装包 (Inno Setup) — 2026-08 实测

`./dev.sh installer` 用 Inno Setup 6 把 `build/windows/x64/runner/Release/` 打成
EXE 安装包 (输出 `build/installer/singbird-setup-<ver>-x64.exe`, gitignored)。
以下坑全部实测踩过。

## 工具安装与路径

- 安装: `winget install JRSoftware.InnoSetup` → **per-user 安装**, ISCC.exe 在
  `%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe` (**不在** Program Files (x86)!)
- dev.sh 的 `cmd_installer` 用 `for cand in ...; do [ -f "$cand" ] && break; done` 探测候选路径

## 关键坑

1. **ISCC 是原生 exe, 吃不了 MSYS 路径**: 传 `/c/Users/.../singbird.iss` 报
   `Unknown option: /c/...` → 传参前 `cygpath -w`。
2. **pipefail + `ls a b c 2>/dev/null | head -1`**: 任一路径不存在, ls 的 exit 2 经
   pipefail 静默杀掉 `set -e` 脚本 (stderr 被 2>/dev/null 吞, 表现为无输出 exit 2)
   → 别用这个模式, 用 `[ -f ]` 循环。
3. **ChineseSimplified.isl 是社区翻译, Inno 安装器不捆绑**: 官方安装只带 29 个语言
   文件, 没有中文。从 https://raw.githubusercontent.com/jrsoftware/issrc/main/
   Files/Languages/ChineseSimplified.isl 下载 (官方 Languages/ 目录, **不是**
   Unofficial/, 那个路径 404) → vendor 到 `installer/languages/` 并在 .iss 用相对
   路径 `MessagesFile: "languages\ChineseSimplified.isl"` 引用 (可复现构建)。
4. **7-Zip (26.01) 打不开 Inno 6.7.3 产物**: `7z l` 报 "Cannot open the file as
   archive", `7z t` 显示 0 files → 包内容验证改用:
   - ISCC 编译日志的 `Compressing: ...` 清单 (权威, 逐文件列出)
   - `grep -ac "Inno Setup Setup Data" setup.exe` (Inno 数据签名, >0 即有效)
   - `powershell (Get-Item ...).VersionInfo.ProductName/ProductVersion` (版本注入确认)
5. **Error 32 输出文件被锁**: 有 `singbird-setup-*.exe` 进程在跑 (用户打开过安装包/
   卡在 UAC) → `tasklist | grep -i setup` 确认; 别盲目 taskkill (可能是用户正在操作),
   等进程退出即可重编译。
6. **运行时产物必须排除**: `Excludes: "singbox.log,monitor.db,*.log"` — 日志/数据库
   运行时生成, 不该进安装包 (还可能有敏感信息)。

## .iss 要点 (installer/singbird.iss)

- `AppId={{<固定GUID>}` (跨版本升级靠它, 生成一次永久不变)
- `PrivilegesRequired=admin` + `DefaultDirName={autopf}\singbird` (应用自身
  requireAdministrator, 配置在 %APPDATA%, 装 Program Files 没问题)
- `Compression=lzma2/max` + `SolidCompression=yes`: ~100MB (含 63MB sing-box.exe)
  压到 26MB
- 版本注入: dev.sh 从 pubspec.yaml 读 `version:` 传 `/DAppVer=` `/DOutName=`
- `[Run]` 启动项加 `skipifsilent` (静默安装不自动启动)

## 金标准验证: 一次 UAC 的 安装→校验→卸载

写 ps1: 静默安装 (`/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /DIR=<临时目录> /LOG=`)
→ 校验必需文件齐全 + 无 log/db → 静默卸载 (`unins000.exe /VERYSILENT`) → 结果写
文本文件; 用 `powershell Start-Process powershell -ArgumentList ... -Verb RunAs -Wait`
只弹**一次** UAC。2026-08 实测: INSTALL_EXIT=0, 21 文件齐全, 63MB 后端 intact,
卸载后目录清理干净。

## dev.sh 集成

- `installer` 命令: 检查 Release 存在 → 探测 ISCC → 读版本 → `ISCC $(cygpath -w ...iss)
  /DAppVer= /DOutName=` → 输出 build/installer/singbird-setup-<ver>-x64.exe
- 依赖: `./dev.sh windows` 的产物 (singbird.exe + sing-box.exe + dlls + data/)
