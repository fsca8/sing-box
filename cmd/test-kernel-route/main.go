//go:build with_netbird

// test-kernel-route 是 kernel_tun (路线A) 的开发验证工具:
// 单独调用 SetupKernelRoute, 验证 ip rule 2000 + table 10021 路由的
// 安装与清理, 不依赖 netbird 凭据/隧道。
//
// 用法:
//  1. 模拟 wt0:  ip link add wt0 type dummy; ip addr add <overlay-ip>/16 dev wt0; ip link set wt0 up
//  2. 运行:      go run -tags with_netbird ./cmd/test-kernel-route [cidr]
//  3. 期间用另一终端检查: ip rule show | grep 10021; ip route show table 10021
//     15 秒后自动清理, 或 Ctrl-C 提前结束。
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/experimental/netbird_integration"
)

func main() {
	// overlay 网段不写默认值：由参数或 TEST_NETBIRD_CIDR 提供（各家 netbird 网络不同）。
	cidr := os.Getenv("TEST_NETBIRD_CIDR")
	if len(os.Args) > 1 {
		cidr = os.Args[1]
	}
	if cidr == "" {
		fmt.Println("usage: test-kernel-route <overlay-cidr>   (or set TEST_NETBIRD_CIDR)")
		os.Exit(2)
	}

	cleanup, err := netbird_integration.SetupKernelRoute(cidr)
	if err != nil {
		fmt.Printf("SETUP_ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("SETUP_OK: rule 2000 + table 10021 installed for %s, holding 15s...\n", cidr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case <-time.After(15 * time.Second):
	}

	cleanup()
	fmt.Println("CLEANUP_OK")
}
