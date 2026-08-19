//go:build with_netbird

package libbox

import "github.com/sagernet/sing-box/experimental/netbird_integration"

// SetIFaceDiscover 注册 Android 网络接口发现器。Kotlin 在引擎启动前调用
// (NetbirdStartAll 之前)。netbird 引擎的 ICE 用该列表生成 host candidate,
// 没有它 Android 上只能走 relay(标准库 netlink 枚举被 SELinux 拒绝)。
func SetIFaceDiscover(d IFaceDiscover) {
	androidIFaceDiscover = d
}

// registerIFaceDiscover 把 Kotlin 注入的发现器桥接给 netbird 引擎
// (embed.SetIFaceDiscover), 必须在 netbird 引擎启动前调用。
func registerIFaceDiscover() {
	if androidIFaceDiscover != nil {
		netbird_integration.SetAndroidIFaceDiscover(androidIFaceDiscover)
		writeMarker("SetIFaceDiscover: registered")
	} else {
		writeMarker("SetIFaceDiscover: none (netbird ICE will be relay-only on Android)")
	}
}
