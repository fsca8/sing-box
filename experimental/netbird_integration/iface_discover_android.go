//go:build android

package netbird_integration

import nbembed "github.com/netbirdio/netbird/client/embed"

// SetAndroidIFaceDiscover 注册 Android 平台的网络接口发现器(ConnectivityManager /
// java NetworkInterface 枚举)。宿主(Kotlin 经 libbox)在引擎启动前注入。
//
// Android 11+ SELinux 禁止 app 直接创建 AF_NETLINK SOCK_RAW socket, 标准库
// net.Interfaces()/Addrs() 必然 EPERM → ICE agent 创建失败 → 无 host 候选 →
// 所有连接退化为 relay。该回调提供绕过 netlink 的真实接口列表, 恢复 P2P。
// 必须早于 Engine.Start()(netbird embed Start)调用。
func SetAndroidIFaceDiscover(d nbembed.IFaceDiscover) {
	nbembed.SetIFaceDiscover(d)
}
