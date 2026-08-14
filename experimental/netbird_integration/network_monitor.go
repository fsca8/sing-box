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
	for {
		time.Sleep(5 * time.Second)
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
		// Exclude the engines' own virtual adapters: sing-box TUN (singtun)
		// and netbird kernel TUN (wt0, Linux). Their creation/flapping is
		// not a physical network change and would otherwise trigger a
		// spurious engine restart right after startup.
		if strings.HasPrefix(ifc.Name, "singtun") || strings.HasPrefix(ifc.Name, "wt0") {
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
