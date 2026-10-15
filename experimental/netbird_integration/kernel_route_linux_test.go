//go:build with_netbird && linux

package netbird_integration

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"
)

// TestParseOverlayPrefix: the overlay network comes from the management
// server and differs per account, so an empty or malformed one is an error —
// guessing a network would install a route for somebody else's overlay. Host
// bits are masked off: routes and rules describe networks, not peers.
func TestParseOverlayPrefix(t *testing.T) {
	prefix, err := parseOverlayPrefix("192.0.2.0/24")
	if err != nil {
		t.Fatalf("parseOverlayPrefix: %v", err)
	}
	if prefix.String() != "192.0.2.0/24" {
		t.Fatalf("prefix = %s, want 192.0.2.0/24", prefix)
	}

	prefix, err = parseOverlayPrefix("192.0.2.7/24")
	if err != nil {
		t.Fatalf("parseOverlayPrefix: %v", err)
	}
	if prefix.String() != "192.0.2.0/24" {
		t.Fatalf("prefix = %s, want 192.0.2.0/24", prefix)
	}

	for _, cidr := range []string{"", "not-a-cidr", "192.0.2.0", "2001:db8::/32"} {
		if _, err := parseOverlayPrefix(cidr); err == nil {
			t.Fatalf("parseOverlayPrefix(%q) succeeded, want an error", cidr)
		}
	}
}

// TestRouteDstIsMasked: route destinations are installed in network form even
// when the reported CIDR carries host bits.
func TestRouteDstIsMasked(t *testing.T) {
	prefix, err := parseOverlayPrefix("192.0.2.7/24")
	if err != nil {
		t.Fatalf("parseOverlayPrefix: %v", err)
	}
	if dst := routeDst(prefix); dst.String() != "192.0.2.0/24" {
		t.Fatalf("routeDst = %s, want 192.0.2.0/24", dst)
	}
}

// stubInterfaceProbe replaces the live interface probe for the duration of a
// test and restores the previous one.
func stubInterfaceProbe(t *testing.T, prefix netip.Prefix, err error) {
	t.Helper()
	previous := overlayPrefixFromInterfaceFn
	overlayPrefixFromInterfaceFn = func() (netip.Prefix, error) {
		return prefix, err
	}
	t.Cleanup(func() { overlayPrefixFromInterfaceFn = previous })
}

// clearInstalledPrefix forgets the prefix this process installed, so a test
// observes a cold start rather than a previous test's leftovers.
func clearInstalledPrefix(t *testing.T) {
	t.Helper()
	kernelRouteState.mu.Lock()
	previous := kernelRouteState.prefix
	kernelRouteState.prefix = netip.Prefix{}
	kernelRouteState.mu.Unlock()
	t.Cleanup(func() {
		kernelRouteState.mu.Lock()
		kernelRouteState.prefix = previous
		kernelRouteState.mu.Unlock()
	})
}

var errProbeUnavailable = errors.New("probe unavailable")

// TestResolveOverlayPrefixPrecedence pins the order of authority: the sync
// response wins when it is usable, the interface is the fallback that makes
// startup independent of the sync response, and the last installed prefix only
// keeps an already-working route alive.
func TestResolveOverlayPrefixPrecedence(t *testing.T) {
	clearInstalledPrefix(t)

	// sync wins over the interface.
	stubInterfaceProbe(t, netip.MustParsePrefix("198.51.100.0/24"), nil)
	prefix, source, err := resolveOverlayPrefix("192.0.2.0/24")
	if err != nil {
		t.Fatalf("resolveOverlayPrefix: %v", err)
	}
	if source != "sync" || prefix.String() != "192.0.2.0/24" {
		t.Fatalf("got %s from %q, want 192.0.2.0/24 from \"sync\"", prefix, source)
	}

	// No usable sync CIDR: the interface decides.
	prefix, source, err = resolveOverlayPrefix("")
	if err != nil {
		t.Fatalf("resolveOverlayPrefix: %v", err)
	}
	if source != "interface" || prefix.String() != "198.51.100.0/24" {
		t.Fatalf("got %s from %q, want 198.51.100.0/24 from \"interface\"", prefix, source)
	}

	// Nothing live, but a prefix was installed earlier in this process.
	rememberPrefix(netip.MustParsePrefix("203.0.113.0/24"))
	stubInterfaceProbe(t, netip.Prefix{}, errProbeUnavailable)
	prefix, source, err = resolveOverlayPrefix("")
	if err != nil {
		t.Fatalf("resolveOverlayPrefix: %v", err)
	}
	if source != "cached" || prefix.String() != "203.0.113.0/24" {
		t.Fatalf("got %s from %q, want 203.0.113.0/24 from \"cached\"", prefix, source)
	}
}

// TestResolveOverlayPrefixUnknown: a cold start with neither a sync CIDR nor a
// usable interface must report the problem instead of inventing a network.
func TestResolveOverlayPrefixUnknown(t *testing.T) {
	clearInstalledPrefix(t)
	stubInterfaceProbe(t, netip.Prefix{}, errProbeUnavailable)

	if _, _, err := resolveOverlayPrefix(""); err == nil {
		t.Fatal("resolveOverlayPrefix with no source succeeded, want an error")
	} else if !strings.Contains(err.Error(), kernelIfaceName) {
		t.Fatalf("error %q does not name the interface it probed", err)
	}
}

// TestSetupKernelRouteRejectsUnknownPrefix: an unusable prefix must fail before
// any system call, and must not hand back a cleanup function that pretends
// something was installed.
func TestSetupKernelRouteRejectsUnknownPrefix(t *testing.T) {
	clearInstalledPrefix(t)
	stubInterfaceProbe(t, netip.Prefix{}, errProbeUnavailable)

	for _, cidr := range []string{"", "not-a-cidr", "2001:db8::/32"} {
		cleanup, err := SetupKernelRoute(cidr)
		if err == nil {
			cleanup()
			t.Fatalf("SetupKernelRoute(%q) succeeded, want an error", cidr)
		}
		if cleanup != nil {
			t.Fatalf("SetupKernelRoute(%q) returned a cleanup alongside an error", cidr)
		}
		if !strings.Contains(err.Error(), "kernel route") {
			t.Fatalf("error %q does not name the kernel route", err)
		}
	}
}

// TestReconcileKernelRouteRejectsUnknownPrefix: the self-heal path must be just
// as unwilling to guess, otherwise a broken startup would be "repaired" onto a
// network that is not the account's.
func TestReconcileKernelRouteRejectsUnknownPrefix(t *testing.T) {
	clearInstalledPrefix(t)
	stubInterfaceProbe(t, netip.Prefix{}, errProbeUnavailable)

	if err := ReconcileKernelRoute(""); err == nil {
		t.Fatal("ReconcileKernelRoute with no prefix source succeeded, want an error")
	}
}

// TestOverlayPrefixFromInterface: the live probe reads the kernel TUN
// interface's own address. Skipped where the tunnel is not up — this is an
// opportunistic check of the ground-truth source, not a fixture.
func TestOverlayPrefixFromInterface(t *testing.T) {
	link, err := netlink.LinkByName(kernelIfaceName)
	if err != nil {
		t.Skipf("%s is not present on this host", kernelIfaceName)
	}
	addrs, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		t.Fatalf("AddrList: %v", err)
	}
	if len(addrs) == 0 {
		t.Skipf("%s has no IPv4 address", kernelIfaceName)
	}
	prefix, err := overlayPrefixFromInterface()
	if err != nil {
		t.Fatalf("overlayPrefixFromInterface: %v", err)
	}
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		t.Fatalf("prefix = %s, want a valid IPv4 prefix", prefix)
	}
	if prefix.Bits() <= 0 || prefix.Bits() >= 32 {
		t.Fatalf("prefix = %s, want a network prefix (not a single address)", prefix)
	}
	if prefix != prefix.Masked() {
		t.Fatalf("prefix = %s, want it masked", prefix)
	}
}
