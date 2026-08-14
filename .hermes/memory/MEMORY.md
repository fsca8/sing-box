gomobile/libbox调试: MSYS滤掉cmd隐藏变量→干净终端复现不了malformed env panic,用原生Go launcher注入=::=::\绕过; 验证须真实包./experimental/libbox; 原生go吃不了/tmp须cygpath -w; gomobile须-androidapi 23。详见framework-pitfalls.md
§
singbird Windows 排查日志地图: 后端自身日志 singbox.log 在 exe 所在目录(cwd: dev Release 或安装目录 C:\Programs\singbird), 不在数据目录; 控制器 ctrl 日志在 %TEMP%\singbird-YYYYMMDD.log(无连字符, AppLogger 轮转保留3); 数据目录=%APPDATA%\io.nekohasekai.sfm\<ProductName>(由 windows/runner/Runner.rc 决定)——更名即换目录旧配置'消失'; 后端 exe 名 sing-box-netbird-v<tag>-<hash>-<date> 可精确对 commit; run.log无exited=_closeLogFile竞态: unawaited close关新sink置null,先final sink再置null
§
MSYS/Inno 打包坑: 原生exe不吃/c/路径先cd或cygpath -w; pipefail下ls失败静默exit2, 多候选用for+[ -f ]; winget InnoSetup per-user, ISCC在%LOCALAPPDATA%\Programs\Inno Setup 6\; ChineseSimplified.isl从jrsoftware/issrc Files/Languages/vendor(非Unofficial); 7-Zip打不开Inno 6.7.3产物, 验证靠ISCC Compressing清单+grep "Inno Setup Setup Data"+VersionInfo; ISCC Error32=输出exe被锁(tasklist/Defender,重试); 静默安装-Verb RunAs ps1一次UAC装→校验→卸
§
对端OnNewOffer session变化自动重建ICE agent无需对端重启; 另: netbird relay(ws)路径v0.76.3+systemd直部署RTT=腿程级(~38ms), TURN(acloud:3478 UDP中继)才是340ms慢路径; Windows引擎久跑状态机退化(dump stat停/对探测零响应)须重启Windows
§
配置双文件: profiles.json的config=JSON字符串(非base64),启动时覆盖sing-box-config.json→改MTU等须改profiles.json; Windows更新UAC进程普通taskkill杀不掉→powershell Start-Process taskkill -Verb RunAs,备份singbird-fix/old-win-backup再替换; adb pull须C:/绝对; RustDesk高频error=API端口21114无监听(见rustdesk-selfhost skill)
§
srflx污染时序坑(另一根因): 引擎首轮STUN探测早于远程rule-sets加载(StartAll在sing-box创建前)→探测落final→proxy→srflx=代理出口IP→候选污染→P2P半通/打洞目标错; 修复:注入控制面IP直连(ip_cidr→direct,解析mgmtURL host,不依赖geoip,幂等); 症状:对端configure endpoint出现代理服务器IP; 诊断:netbird-config.json log_level改debug看discovered local candidate srflx
§
netbird 打洞被 TUN 劫持: Linux 侧 68d02fa85 fwmark(0x1BD00)→main 修复(仅Linux); Windows netstack 无 fwmark, STUN 被 sing-box TUN 劫持→srflx缺失(非污染/非对称NAT)→P2P不可能。判据: Clash API STUN(3478/udp) up>>down 且 src=172.19.0.1。process_path→direct 不足(仍进TUN); 加 STUN /32 路由只救STUN救不了P2P(打对端动态IP); 正解=控制面socket绑物理网卡(IP_UNICAST_IF)。installControlPlaneMarkRule 无Win stub→补 engine_rules_other.go(!linux||android)
§
sing-box dev.sh默认GOOS=windows(143行),Linux构建须GOOS=linux否则出PE/Exec format error