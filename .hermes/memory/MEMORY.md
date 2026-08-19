gomobile/libbox调试: malformed env panic须原生Go launcher注入=::=::\绕过; 验证须真实包; go吃不了/tmp须cygpath -w; -androidapi 23
§
singbird Windows排查日志地图: 后端日志 singbox.log 在 exe 所在目录(cwd: dev Release 或 C:\Programs\singbird), 不在数据目录; 控制器日志 %TEMP%\singbird-YYYYMMDD.log(无连字符,轮转保留3); 数据目录=%APPDATA%\io.nekohasekai.sfm\<ProductName>(Runner.rc决定)——更名即换目录旧配置'消失'; 后端 exe 名 sing-box-netbird-v<tag>-<hash>-<date> 精确对 commit; run.log无exited=_closeLogFile竞态: unawaited close关新sink置null,先final sink再置null
§
MSYS/Inno打包: 原生exe不吃/c/路径(cygpath -w); pipefail下ls失败exit2用for+[ -f ]; InnoSetup per-user,ISCC在%LOCALAPPDATA%\Programs\Inno Setup 6\; ChineseSimplified.isl从issrc Files/Languages/vendor; 7-Zip打不开Inno 6.7.3产物,验证靠ISCC清单+grep "Inno Setup Setup Data"; ISCC Error32=输出exe被锁; 静默安装-Verb RunAs ps1
§
对端OnNewOffer session变化自动重建ICE agent无需重启; netbird relay(ws)直部署RTT~38ms,TURN(acloud:3478 UDP中继)340ms慢路径; Windows引擎久跑状态机退化须重启
§
配置双文件: profiles.json的config=JSON字符串,启动覆盖sing-box-config.json→改MTU等须改profiles.json; UAC进程普通taskkill杀不掉→Start-Process taskkill -Verb RunAs,先备份old-win-backup再替换; adb pull须C:/绝对; 配置编辑器用JsonEncoder转义(918810f前手写序列化器把反斜杠路径写坏成非法JSON)
§
srflx污染时序坑(另一根因): 引擎首轮STUN探测早于远程rule-sets加载(StartAll在sing-box创建前)→探测落final→proxy→srflx=代理出口IP→候选污染→P2P半通/打洞目标错; 修复:注入控制面IP直连(ip_cidr→direct,解析mgmtURL host); 症状:对端configure endpoint出现代理服务器IP; 诊断:log_level=debug看discovered local candidate srflx
§
netbird 打洞被 TUN 劫持: Linux fwmark(0x1BD00)→main 修复(仅Linux); Windows netstack 无 fwmark, STUN 被 sing-box TUN 劫持→srflx缺失→P2P不可能。判据: Clash API STUN up>>down 且 src=172.19.0.1; 正解=控制面socket绑物理网卡(IP_UNICAST_IF)
§
sing-box dev.sh默认GOOS=windows,Linux构建须GOOS=linux否则PE
§
singbird APK构建: 必带--split-per-abi(Flutter插件dummy CMake默认全ABI configure); kill构建后rm -rf build/.cxx否则daemon CMake持锁报文件占用
§
netbird域名/CIDR实时注入: rule-set声明须显式format=source(1.14 url.Parse推断扩展名,Windows反斜杠盘符路径被当scheme→path空→missing format); 文件须先于加载存在; fswatch重写即生效勿rename; 静态CIDR规则已移除改nb-cidr文件承载(a8bc0dde6); process_path旁路规则死代码(3381b1fb9移除)残留可删
§
sing-box: DNS路由与连接路由独立,域名直连≠其DNS走dns-direct,查询只按dns.rules匹配(不中走默认);直连outbound解析域名经resolveDialer→Lookup仍按DNS规则;fakeip直连反查假IP后Lookup(allowFakeIP=false)跳过fakeip规则取真实IP;要让直连域名用dns-direct须在dns.rules镜像连接规则