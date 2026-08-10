---
name: adb-backup-extract
description: 从非 debuggable 的 Android 应用(release 版)提取私有数据(monitor.db/profiles.json 等)——adb backup 官方通道,无需装 debug 版或 root。适用于 singbird 及任意 Android 调试。
---

# adb backup 提取非 debuggable 应用数据

## 触发场景
- 目标是 release/非 debuggable APK(debuggable=false),`run-as` 被拒
- 不想装 debug 版覆盖(丢数据/要重编译),又需要读 app 私有目录的配置/数据库
- 实测:singbird release 版拿 monitor.db / profiles.json / netbird-config.json

## 步骤

1. **备份**(设备需解锁并点"备份我的数据"确认;Android 8.1 实测可行,有 deprecated 警告属正常):
```bash
adb backup -f out.ab -noapk <package>
# 例: adb backup -f singbird.ab -noapk io.nekohasekai.sfm.singbird
```

2. **解析 backup.ab**(Python;格式 = 4 行 `\n` 结尾 header + zlib 压缩的 tar):
```python
import zlib, tarfile
with open('out.ab','rb') as f:
    for _ in range(4): f.readline()   # ANDROID BACKUP / version / compression / encryption
    payload = f.read()
open('backup.tar','wb').write(zlib.decompress(payload))
tf = tarfile.open('backup.tar')
for m in tf.getmembers():
    if 'monitor.db' in m.name: print(m.name)
tf.extractall(path='.')
# 私有文件在 apps/<pkg>/f/<filename> 下
```

## 坑
- **header 是 4 行 \n 分隔,不是 \0**——按 \0 切分会导致 zlib "incorrect header check"
- 未加密且未压缩时为 tar;加密(密码备份)需 abe 工具,这里用不到
- 备份是完整快照:包含 files/ 下所有文件(配置、SQLite、日志)
- 与 `adb exec-out` 类似:二进制(如 SQLite)经 shell 文本通道会损坏,backup 通道本身是二进制安全
