package libbox

// IFaceDiscover 由宿主(Android Kotlin)注入: 提供真实网络接口枚举
// (java NetworkInterface / ConnectivityManager), 替代被 Android SELinux
// 禁止的标准库 netlink 枚举。
//
// 返回格式(与 netbird stdnet.parseInterfacesString 匹配, 每行一个接口):
//
//	<name> <index> <mtu> <up> <broadcast> <loopback> <pointToPoint> <multicast>|<addr1/prefix> <addr2/prefix> ...
//
// 例如:
//
//	wlan0 3 1500 true true false false true|192.168.1.5/24 fe80::1/64
type IFaceDiscover interface {
	IFaces() (string, error)
}

var androidIFaceDiscover IFaceDiscover
