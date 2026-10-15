//go:build with_netbird && linux

package netbird_integration

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"

	"github.com/sagernet/netlink"
	"github.com/sagernet/sing-box/log"
	"golang.org/x/sys/unix"
)

// Kernel-TUN (Route A) static route constants.
//
// The rule priority must be numerically LOWER than sing-tun's rule index
// (default 9000) so overlay traffic is matched here first and never reaches the
// sing-box TUN stack. The table is deliberately dedicated: sing-tun uses 2022,
// and reusing it would make the overlay route compete with the proxy's default
// route.
const (
	kernelRoutePriority = 2000
	kernelRouteTable    = 10021
	kernelIfaceName     = "wt0"
)

// kernelRouteState remembers the overlay prefix that is actually installed, so
// a later reconcile can reuse it when a sync response carries no CIDR (an
// unrelated sync hiccup must not tear down a working route) and a
// management-side renumber can drop the stale one.
var kernelRouteState struct {
	mu     sync.Mutex
	prefix netip.Prefix
}

// parseOverlayPrefix validates the overlay CIDR reported by the management
// server. 网段必须来自 netbird 同步结果（各账号不同）：空/非法直接报错，不用猜测
// 的网段建路由。返回值已掩掉主机位（规则/路由里的网段必须是网络地址）。
func parseOverlayPrefix(networkCIDR string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(networkCIDR)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid or missing overlay CIDR %q: %w", networkCIDR, err)
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("IPv4 required, got %s", prefix)
	}
	return prefix.Masked(), nil
}

// overlayPrefixFromInterface derives the overlay prefix from the kernel TUN
// interface's own IPv4 address and netmask.
//
// This is ground truth: netbird configures the interface with the peer's own
// overlay address and the network's prefix length (e.g. "<addr>/16"), so the
// masked address *is* the account network. Deriving it here removes the
// dependency on the management sync response, which on a real deployment can
// deliver the DNS half of the network map while never carrying a usable
// CIDR — and a kernel route that is never installed leaves overlay traffic
// (and the supervision probe that checks for it) permanently broken.
func overlayPrefixFromInterface() (netip.Prefix, error) {
	link, err := netlink.LinkByName(kernelIfaceName)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("find %s: %w", kernelIfaceName, err)
	}
	addrs, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("list %s addresses: %w", kernelIfaceName, err)
	}
	for _, a := range addrs {
		if a.IPNet == nil || a.IPNet.IP == nil || a.IPNet.IP.IsUnspecified() {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.IPNet.IP.To4())
		if !ok {
			continue
		}
		ones, bits := a.IPNet.Mask.Size()
		// A /32 (or a missing mask) describes the peer, not the network: it
		// must not be turned into an overlay route.
		if bits != 32 || ones <= 0 || ones >= 32 {
			continue
		}
		return netip.PrefixFrom(ip, ones).Masked(), nil
	}
	return netip.Prefix{}, fmt.Errorf("%s has no usable IPv4 address yet", kernelIfaceName)
}

// overlayPrefixFromInterfaceFn is the interface probe used by
// resolveOverlayPrefix. A variable so tests can pin the precedence rules
// without a live tunnel; production code never reassigns it.
var overlayPrefixFromInterfaceFn = overlayPrefixFromInterface

// resolveOverlayPrefix picks the overlay prefix to install and reports which
// source won, in decreasing order of authority:
//
//  1. "sync"      the CIDR carried by the management sync response.
//  2. "interface" derived from the kernel TUN interface's own address — the
//     same source the tunnel DNS address uses, so it is exactly as reliable
//     as the data path itself.
//  3. "cached"    the last prefix this process installed, so a sync hiccup
//     cannot tear down a working route. Never used to invent a network on a
//     cold start.
func resolveOverlayPrefix(networkCIDR string) (netip.Prefix, string, error) {
	syncPrefix, syncErr := parseOverlayPrefix(networkCIDR)
	if syncErr == nil {
		return syncPrefix, "sync", nil
	}
	ifacePrefix, ifaceErr := overlayPrefixFromInterfaceFn()
	if ifaceErr == nil {
		return ifacePrefix, "interface", nil
	}
	kernelRouteState.mu.Lock()
	cached := kernelRouteState.prefix
	kernelRouteState.mu.Unlock()
	if cached.IsValid() {
		return cached.Masked(), "cached", nil
	}
	return netip.Prefix{}, "", fmt.Errorf(
		"overlay prefix unknown: sync CIDR %q rejected (%v); %s unusable (%v)",
		networkCIDR, syncErr, kernelIfaceName, ifaceErr)
}

// rememberPrefix records the prefix this process installed, for the "cached"
// fallback above.
func rememberPrefix(prefix netip.Prefix) {
	kernelRouteState.mu.Lock()
	kernelRouteState.prefix = prefix
	kernelRouteState.mu.Unlock()
}

// routeDst converts an overlay prefix into netlink's route destination form.
func routeDst(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   prefix.Masked().Addr().AsSlice(),
		Mask: net.CIDRMask(prefix.Bits(), 32),
	}
}

// RefreshKernelDNSAddr reads the kernel TUN interface's live IPv4 address and
// records it as the tunnel DNS server address (host:port) used by the
// "netbird" DNS transport. It returns the address it recorded.
//
// Kernel-TUN mode cannot derive this address from the overlay CIDR the way
// netstack mode does (see ComputeDNSAddr): netbird's in-process DNS server
// binds the WG interface's own overlay address on netbirdDNSPort. That address
// only exists once netbird has finished bringing the interface up, which can
// happen after the startup snapshot was taken — and happens again after every
// engine restart — so it has to be read live instead of cached once.
//
// Returns an error (rather than silently doing nothing) when the interface has
// no IPv4 address yet: the caller can then retry, and the failure is visible.
func RefreshKernelDNSAddr() (string, error) {
	link, err := netlink.LinkByName(kernelIfaceName)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", kernelIfaceName, err)
	}
	addrs, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return "", fmt.Errorf("list %s addresses: %w", kernelIfaceName, err)
	}
	for _, a := range addrs {
		if a.IPNet == nil || a.IPNet.IP == nil || a.IPNet.IP.IsUnspecified() {
			continue
		}
		addr := net.JoinHostPort(a.IPNet.IP.String(), strconv.Itoa(netbirdDNSPort))
		SetDNSAddr(addr)
		return addr, nil
	}
	return "", fmt.Errorf("%s has no IPv4 address yet", kernelIfaceName)
}

// kernelRouteInstalled reports whether the dedicated table already carries the
// overlay route on the given interface. Replacing an unchanged route sends the
// kernel (and every route watcher, including netbird's own network monitor) a
// route-change notification for nothing, so the write path probes first.
func kernelRouteInstalled(prefix netip.Prefix, linkIndex int) bool {
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: kernelRouteTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return false
	}
	want := routeDst(prefix).String()
	for _, route := range routes {
		if route.LinkIndex == linkIndex && route.Dst != nil && route.Dst.String() == want {
			return true
		}
	}
	return false
}

// kernelRuleInstalled reports whether the rule that steers the overlay CIDR
// into the dedicated table is present with the expected priority.
func kernelRuleInstalled(prefix netip.Prefix) bool {
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if rule.Priority == kernelRoutePriority && rule.Table == kernelRouteTable && rule.Dst == prefix {
			return true
		}
	}
	return false
}

// ensureKernelRoute installs the overlay route and rule for the live
// interface and reports whether it had to write anything. Both writes are
// idempotent and skipped when the state is already correct (the rule add
// additionally tolerates EEXIST, which the kernel reports without modifying
// anything), so calling this repeatedly is cheap and does not churn the route
// tables.
func ensureKernelRoute(prefix netip.Prefix) (bool, error) {
	link, err := netlink.LinkByName(kernelIfaceName)
	if err != nil {
		return false, fmt.Errorf("find %s: %w", kernelIfaceName, err)
	}
	linkIndex := link.Attrs().Index
	changed := false

	if !kernelRouteInstalled(prefix, linkIndex) {
		route := &netlink.Route{
			LinkIndex: linkIndex,
			Dst:       routeDst(prefix),
			Table:     kernelRouteTable,
		}
		if err := netlink.RouteReplace(route); err != nil {
			return false, fmt.Errorf("add route to table %d: %w", kernelRouteTable, err)
		}
		changed = true
	}

	if !kernelRuleInstalled(prefix) {
		rule := netlink.NewRule()
		rule.Priority = kernelRoutePriority
		rule.Table = kernelRouteTable
		rule.Dst = prefix
		rule.Family = unix.AF_INET
		if err := netlink.RuleAdd(rule); err != nil && !errors.Is(err, syscall.EEXIST) {
			return false, fmt.Errorf("add rule priority %d: %w", kernelRoutePriority, err)
		}
		changed = true
	}
	return changed, nil
}

// removeKernelRoute deletes the rule and the dedicated-table route for prefix.
//
// The delete is rebuilt from the prefix instead of reusing the objects captured
// at install time: the interface can be recreated in between (new link index),
// and a stale index would silently fail to delete anything.
func removeKernelRoute(prefix netip.Prefix) {
	rule := netlink.NewRule()
	rule.Priority = kernelRoutePriority
	rule.Table = kernelRouteTable
	rule.Dst = prefix
	rule.Family = unix.AF_INET
	if err := netlink.RuleDel(rule); err != nil && !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ESRCH) {
		log.Warn("netbird kernel route: delete rule: ", err)
	}

	route := &netlink.Route{Dst: routeDst(prefix), Table: kernelRouteTable}
	// Link index is only needed to identify the route on older kernels; a
	// missing interface is fine (its routes are gone with it).
	if link, err := netlink.LinkByName(kernelIfaceName); err == nil {
		route.LinkIndex = link.Attrs().Index
	}
	if err := netlink.RouteDel(route); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT) {
		log.Warn("netbird kernel route: delete route: ", err)
	}
}

// SetupKernelRoute installs the single static route that sends the netbird
// overlay CIDR straight to the netbird kernel TUN, bypassing the sing-box TUN
// stack (Route A), and records the tunnel DNS address. It must be called after
// the netbird engine has created its interface; the sing-box TUN may be up or
// down since our rule always precedes sing-tun's.
//
// networkCIDR may be empty (the sync response does not always carry it): the
// prefix is then derived from the interface itself, see resolveOverlayPrefix.
// The call fails only when no prefix can be established at all — the periodic
// reconcile retries it.
//
// Returns a cleanup function that removes the route and rule.
func SetupKernelRoute(networkCIDR string) (func(), error) {
	prefix, source, err := resolveOverlayPrefix(networkCIDR)
	if err != nil {
		return nil, fmt.Errorf("netbird kernel route: %w", err)
	}
	changed, err := ensureKernelRoute(prefix)
	if err != nil {
		return nil, fmt.Errorf("netbird kernel route: %w", err)
	}
	rememberPrefix(prefix)
	if changed {
		log.Info(fmt.Sprintf("netbird kernel route: overlay %s → %s installed (prefix source: %s)", prefix, kernelIfaceName, source))
	}
	if _, err := RefreshKernelDNSAddr(); err != nil {
		// Not fatal: the address is re-read on every query (and on every engine
		// restart), so an interface that is still coming up heals itself
		// instead of leaving the DNS transport permanently without an address.
		log.Warn("netbird kernel route: tunnel DNS address not available yet: ", err)
	}
	return func() { removeKernelRoute(prefix) }, nil
}

// ReconcileKernelRoute re-establishes the kernel-mode wiring for the current
// interface: the overlay route/rule and the tunnel DNS address.
//
// This is the repair path for what a one-shot startup snapshot cannot keep
// true. The interface is destroyed and recreated by every engine (re)start,
// which drops the link-bound route in the dedicated table while the routing
// rule survives (leaving an empty table behind), and the tunnel DNS address is
// the interface's own IPv4, which only exists once it is up. Without this, a
// process that took its snapshot too early stays broken for its whole
// lifetime: the DNS transport has no address, so it never dials anything and
// every custom-domain lookup dies — and the overlay route that supervision
// checks for is missing as well.
//
// networkCIDR is the current sync response's overlay CIDR; an empty value is
// expected (see resolveOverlayPrefix) and falls back to the interface, then to
// the last successfully installed prefix. Safe to call repeatedly: the install
// path probes before writing.
func ReconcileKernelRoute(networkCIDR string) error {
	prefix, source, err := resolveOverlayPrefix(networkCIDR)
	if err != nil {
		return fmt.Errorf("netbird kernel route: %w", err)
	}
	kernelRouteState.mu.Lock()
	previous := kernelRouteState.prefix
	kernelRouteState.mu.Unlock()
	if previous.IsValid() && previous != prefix {
		// The overlay was renumbered (or the prefix source changed): the old
		// rule would keep steering a network that no longer exists.
		removeKernelRoute(previous)
	}
	changed, err := ensureKernelRoute(prefix)
	if err != nil {
		return fmt.Errorf("netbird kernel route: %w", err)
	}
	rememberPrefix(prefix)
	if changed {
		log.Info(fmt.Sprintf("netbird kernel route: overlay %s → %s installed (prefix source: %s, previous %s)", prefix, kernelIfaceName, source, previous))
	}
	if _, err := RefreshKernelDNSAddr(); err != nil {
		return fmt.Errorf("netbird kernel route: %w", err)
	}
	return nil
}
