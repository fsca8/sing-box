//go:build with_netbird && !linux

package netbird_integration

import "errors"

// SetupKernelRoute is a no-op on non-Linux platforms. Kernel-TUN mode is
// Linux-only (netbird creates a real TUN interface there); Windows/Android
// always use the userspace netstack path where this route is unnecessary.
func SetupKernelRoute(networkCIDR string) (func(), error) {
	return func() {}, nil
}

// ReconcileKernelRoute is a no-op on non-Linux platforms: there is no kernel
// route or interface to repair, and netstack mode recomputes the tunnel DNS
// address from the overlay CIDR on every engine (re)start.
func ReconcileKernelRoute(cidr string) error { return nil }

// RefreshKernelDNSAddr is unsupported on non-Linux platforms: netstack mode
// does not bind a host address for its DNS server (see ComputeDNSAddr), so
// there is nothing to read back from the system.
func RefreshKernelDNSAddr() (string, error) {
	return "", errors.New("kernel TUN mode is Linux-only")
}
