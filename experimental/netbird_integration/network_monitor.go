//go:build with_netbird

package netbird_integration

import (
	"net"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// watchNetworkChanges polls the host's non-loopback interface addresses and
// restarts the netbird engine when they change (WiFi ↔ hotspot ↔ WiFi, VPN
// toggle, wired switch). The embedded engine's own network monitor is
// skipped in netstack mode (client/internal/engine.go startNetworkMonitor:
// `|| nbnetstack.IsEnabled()`), so without this the ICE candidates go stale
// after a network switch and the tunnel silently stays on the relay path
// (~350ms RTT vs ~19ms P2P) until a manual app restart.
//
// Polling (5s) instead of OS route-change notifications keeps this free of
// netbird's internal packages (Go internal-import rule) and cross-platform.
// A 5s debounce avoids restart storms from transient flaps.
func watchNetworkChanges(e *Engine) {
	log.Info("netbird: network monitor started (interface addresses, 5s poll)")
	last := addrsSnapshot()
	lastRestart := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	tickCount := 0
	var kernelErr string
	var kernelWarnAt time.Time
	for {
		select {
		case <-e.stopCh:
			// Engine.Shutdown() closed stopCh: the host service is going
			// away. Exit now so the recovery loop cannot resurrect the
			// engine after teardown (leaked watcher + second engine with
			// the same WireGuard identity = stop→start breaks netbird).
			log.Info("netbird: network monitor stopped")
			return
		case <-ticker.C:
		}
		tickCount++
		cur := addrsSnapshot()
		changed := cur != last
		if changed {
			last = cur
		}
		if time.Since(lastRestart) < 5*time.Second {
			continue // debounce transient flaps
		}
		lastRestart = time.Now()
		if !e.IsRunning() {
			// Recovery path: the engine died (e.g. a restart failed because
			// the network was still settling and the management dial timed
			// out). Once the network has settled, bring it back instead of
			// leaving the tunnel dead until a manual app restart.
			log.Info("netbird: engine not running, attempting recovery")
			e.restartOnNetworkChange()
			continue
		}
		if changed {
			log.Info("netbird: network change detected (interface addresses changed), restarting engine")
			e.restartOnNetworkChange()
		}
		// 内核模式自愈(每 5s): wt0 由引擎创建/销毁, 重建时旧路由随接口消失而
		// 路由规则会留下(空表); 隧道 DNS 地址也是接口自己的 IPv4。启动时的一次性
		// 快照抓不到这些, 只能在这里补, 且必须与同步结果解耦 —— 同步失败正是需要
		// 自愈的场景。Reconcile 内部先探测再写, 稳态下不产生任何路由表改动。
		// 失败按“消息指纹 + 60s”限流, 避免接口尚未就绪时刷屏。
		if IsKernelMode() {
			if err := ReconcileKernelRoute(""); err != nil {
				msg := err.Error()
				if msg != kernelErr || time.Since(kernelWarnAt) > time.Minute {
					log.Warn("netbird: reconcile kernel route: ", err)
					kernelErr = msg
					kernelWarnAt = time.Now()
				}
			} else {
				kernelErr = ""
			}
		}
		// 周期性域名刷新(每 30s): 覆盖"引擎已启动但 StartAll 的 5s
		// sync 等待超时"与"管理端新增/移除域名"的场景。refreshDomainsFile
		// 内部按指纹去重, 内容未变不重写文件、不触发 sing-box 重载。
		if tickCount%6 == 0 {
			e.refreshDomainsFromSync()
		}
	}
}

// addrsSnapshot returns a sorted fingerprint of all non-loopback, non
// link-local interface addresses — a cheap cross-platform network signature.
func addrsSnapshot() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Exclude the engines' own virtual adapters: sing-box TUN (singtun,
		// tun0 on Android via VpnService) and netbird kernel TUN (wt0, Linux).
		// Their creation/flapping is not a physical network change and would
		// otherwise trigger a spurious engine restart right after startup.
		if strings.HasPrefix(ifc.Name, "singtun") ||
			strings.HasPrefix(ifc.Name, "wt0") ||
			strings.HasPrefix(ifc.Name, "tun0") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			s := a.String()
			if strings.HasPrefix(s, "169.254.") || strings.HasPrefix(s, "fe80:") {
				continue // link-local noise
			}
			parts = append(parts, ifc.Name+"="+s)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
