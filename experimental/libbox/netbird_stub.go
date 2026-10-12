//go:build !with_netbird

package libbox

import E "github.com/sagernet/sing/common/exceptions"

// NetbirdStartAll — stub when netbird is not compiled in.
func NetbirdStartAll(netbirdConfigJSON string, singBoxConfig string) (string, error) {
	return singBoxConfig, E.New("netbird not compiled in (build without with_netbird tag)")
}

// NetbirdStop — stub when netbird is not compiled in.
func NetbirdStop() {}

// NetbirdBridgeTCP — stub when netbird is not compiled in. 必须与带 tag 的
// 真实实现保持同一套导出符号：否则按 tag 生成绑定（gomobile/JNI）时该方法
// 直接不存在，调用侧是 NoSuchMethod/链接错误而不是可降级的报错。
func NetbirdBridgeTCP(port int, target string) (errStr string) {
	return "netbird not compiled in (build without with_netbird tag)"
}

// NetbirdStopBridge — stub when netbird is not compiled in.
func NetbirdStopBridge(port int) (errStr string) {
	return "netbird not compiled in (build without with_netbird tag)"
}
