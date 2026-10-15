//go:build with_netbird

package netbird_integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	mdns "github.com/miekg/dns"
	nbembed "github.com/netbirdio/netbird/client/embed"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

// netbirdDNSPort is the DNS server port inside the netbird tunnel.
const netbirdDNSPort = 53

// Bounds for one netbird DNS exchange.
//
// The caller's context is deliberately not the only limit: sing-box can hand
// this transport a context without a usable deadline, and an unanswered
// overlay query must not outlive the query itself. Losing that floor is how a
// single missing address turned into permanently stuck lookups.
const (
	netbirdDNSTimeout        = 3 * time.Second
	netbirdDNSAddrAttempts   = 3
	netbirdDNSAddrRetryDelay = 200 * time.Millisecond
	netbirdDNSWarnInterval   = 30 * time.Second
)

// globalClient stores the netbird embed client reference for DNS resolution
// plus the tunnel DNS server address (computed from the account network CIDR).
// Set by SetClient()/SetDNSAddr() after embed Start(), read by Transport.Exchange().
var globalClient struct {
	mu      sync.Mutex
	client  *nbembed.Client
	dnsAddr string
}

func SetClient(c *nbembed.Client) {
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	globalClient.client = c
}

func GetClient() *nbembed.Client {
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	return globalClient.client
}

// ClearClient drops the global embed client reference. Called by
// Engine.Shutdown() so nb-out / the DNS transport never dial through a
// stopped engine after service teardown.
func ClearClient() {
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	globalClient.client = nil
}

// SetDNSAddr records the netbird tunnel DNS server address (host:port).
func SetDNSAddr(addr string) {
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	globalClient.dnsAddr = addr
}

// GetDNSAddr returns the netbird tunnel DNS server address (host:port).
func GetDNSAddr() string {
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	return globalClient.dnsAddr
}

// ComputeDNSAddr derives the netbird DNS server address from the account's
// overlay network CIDR. Netbird runs its DNS server on the last-but-one IP of
// the overlay network (GetLastIPFromNetwork(network, 1) in client/net/net.go),
// e.g. an overlay /16 <net>.0.0/16 → <net>.255.254:53.
//
// Netstack mode only: it is a fake address that the userspace netstack routes
// into the engine. Kernel-TUN mode binds the kernel interface's own address
// instead — see RefreshKernelDNSAddr.
//
// CIDR 由 SyncResponse 的网络信息（或 peer 自身地址）推导；拿不到 CIDR 时
// 返回 ""（没有可用 overlay 网络，猜一个网段没有意义），由调用方跳过该 DNS。
func ComputeDNSAddr(networkCIDR string) string {
	if networkCIDR == "" {
		return ""
	}
	prefix, err := netip.ParsePrefix(networkCIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() >= 32 {
		return ""
	}
	// broadcast = network | ^mask; DNS = broadcast - 1 (same as GetLastIPFromNetwork(_, 1))
	netU32 := binary.BigEndian.Uint32(prefix.Masked().Addr().AsSlice())
	hostBits := 32 - prefix.Bits()
	broadcast := netU32 + (uint32(1)<<hostBits - 1)
	dnsU32 := broadcast - 1
	var dnsIP [4]byte
	binary.BigEndian.PutUint32(dnsIP[:], dnsU32)
	return net.JoinHostPort(netip.AddrFrom4(dnsIP).String(), fmt.Sprint(netbirdDNSPort))
}

// DNSTransportType is the sing-box DNS transport type used in config.
const DNSTransportType = "netbird"

// RegisterTransport registers the netbird DNS transport with sing-box's DNS
// transport registry. Called from an init() in include/ under with_netbird tag.
func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.LocalDNSServerOptions](registry, DNSTransportType, NewTransport)
}

var _ adapter.DNSTransport = (*Transport)(nil)

// Transport resolves DNS queries through netbird's engine DNS handler chain:
// CustomZone (overlay-private) names come back as overlay addresses.
//
// sing-box selects this transport only for names in the injected nb-domains
// rule-set (the injected config never makes it the final resolver), so failing
// here means "the private zone did not resolve" and must be reported as such —
// see Exchange for why it is never retried against a public resolver.
type Transport struct {
	tag     string
	started bool
	closeCh chan struct{}
}

func NewTransport(ctx context.Context, logger log.ContextLogger, tag string, options option.LocalDNSServerOptions) (adapter.DNSTransport, error) {
	return &Transport{
		tag:     tag,
		closeCh: make(chan struct{}),
	}, nil
}

func (t *Transport) Type() string           { return DNSTransportType }
func (t *Transport) Tag() string            { return t.tag }
func (t *Transport) Dependencies() []string { return nil }

func (t *Transport) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	t.started = true
	return nil
}

func (t *Transport) Close() error {
	close(t.closeCh)
	t.started = false
	return nil
}

func (t *Transport) Reset() {}

// Exchange resolves a DNS query through netbird's engine DNS handler.
//
// There is deliberately no public-DNS fallback. Only overlay-private names
// reach this transport, and a public resolver cannot answer them: the fallback
// turned a local failure into a public NXDOMAIN (a wrong, cacheable answer)
// and — because sing-box's own TUN recaptures host-originated DNS and its
// config hijacks it (auto_route + hijack-dns), which selects this same
// transport again for nb-domains — it recursed into itself with no base case,
// leaking one hung socket per round and answering nothing at all.
//
// A failure is reported as SERVFAIL instead: the client gets a fast, correct
// error, and nothing about a private zone leaves the host.
func (t *Transport) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	client := GetClient()
	if client == nil {
		warnNetbirdDNS(message, "", "engine not available")
		return servFailResponse(message), nil
	}
	start := time.Now()
	resp, err := t.resolveViaNetbird(ctx, client, message)
	if err != nil {
		warnNetbirdDNS(message, GetDNSAddr(), fmt.Sprintf("%v (after %s)", err, time.Since(start).Round(time.Millisecond)))
		return servFailResponse(message), nil
	}
	if resp == nil {
		warnNetbirdDNS(message, GetDNSAddr(), "engine returned an empty response")
		return servFailResponse(message), nil
	}
	if resp.Rcode == mdns.RcodeRefused {
		// The engine answered but does not own this name (typically a stale
		// rule-set entry after a management-side change). Report its verdict
		// as-is; do not leak the name to a public resolver to "fix" it.
		warnNetbirdDNS(message, GetDNSAddr(), "engine refused the name (stale custom-zone entry?)")
	}
	return resp, nil
}

// ExchangeAsync wraps Exchange in a goroutine.
func (t *Transport) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(response *mdns.Msg, err error)) {
	go func() {
		resp, err := t.Exchange(ctx, message)
		callback(resp, err)
	}()
}

// resolveViaNetbird sends the DNS query through the netbird tunnel to the
// tunnel's DNS server. Netstack mode uses the netbird client's DialContext
// (wgnetstack) — the only path that reaches netbird's in-process DNS handler
// there, a bare net.Dialer would use the host network stack and never enter
// the tunnel. Kernel-TUN mode has no wgnetstack and dials the resolver's
// kernel-side address through the host stack instead; the overlay route
// delivers the packet to wt0.
func (t *Transport) resolveViaNetbird(ctx context.Context, client *nbembed.Client, message *mdns.Msg) (*mdns.Msg, error) {
	dnsAddr, err := ensureNetbirdDNSAddr()
	if err != nil {
		return nil, err
	}

	// Hard bound for this attempt, independent of the caller's context: the
	// deadline is what keeps a lost packet from parking the goroutine (and its
	// socket) forever.
	ctx, cancel := context.WithTimeout(ctx, netbirdDNSTimeout)
	defer cancel()

	var conn net.Conn
	if IsKernelMode() {
		conn, err = (&net.Dialer{Timeout: netbirdDNSTimeout}).DialContext(ctx, "udp", dnsAddr)
	} else {
		conn, err = client.DialContext(ctx, "udp", dnsAddr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", dnsAddr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	co := &mdns.Conn{Conn: conn}
	defer co.Close()
	if err := co.WriteMsg(message); err != nil {
		return nil, fmt.Errorf("write to %s: %w", dnsAddr, err)
	}
	resp, err := co.ReadMsg()
	if err != nil {
		return nil, fmt.Errorf("read from %s: %w", dnsAddr, err)
	}
	return resp, nil
}

// ensureNetbirdDNSAddr returns the tunnel DNS server's address, resolving it
// on demand when it is not known yet.
//
// Kernel-TUN mode has no usable cached value to fall back on: netbird's
// in-process DNS server binds the kernel interface's own overlay address,
// which the kernel only assigns once the interface is up — possibly after the
// startup snapshot, and again after every engine restart. Reading it live (with
// a short retry for an interface that is still coming up) is what makes
// custom-domain resolution survive that; trusting a single startup snapshot
// left the transport with no address at all, so it never dialled anything and
// every lookup died on the fallback path.
func ensureNetbirdDNSAddr() (string, error) {
	if !IsKernelMode() {
		// Netstack mode: the engine computed the address from the overlay CIDR
		// when it synced (ComputeDNSAddr).
		if addr := GetDNSAddr(); addr != "" {
			return addr, nil
		}
		return "", fmt.Errorf("tunnel DNS address not set (engine has not synced yet)")
	}
	var lastErr error
	for attempt := 0; attempt < netbirdDNSAddrAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(netbirdDNSAddrRetryDelay)
		}
		addr, err := RefreshKernelDNSAddr()
		if err == nil && addr != "" {
			return addr, nil
		}
		lastErr = err
	}
	// The interface read can lose a race with an engine restart; a previously
	// recorded address is still correct for a peer whose overlay address does
	// not change, so prefer it over failing outright.
	if addr := GetDNSAddr(); addr != "" {
		return addr, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kernel tunnel interface has no IPv4 address")
	}
	return "", fmt.Errorf("kernel tunnel DNS address unavailable: %w", lastErr)
}

// servFailResponse builds a SERVFAIL answer for request, echoing its question
// so the caller can match it up. It is the only failure this transport reports:
// see Exchange.
func servFailResponse(request *mdns.Msg) *mdns.Msg {
	resp := new(mdns.Msg)
	resp.SetRcode(request, mdns.RcodeServerFailure)
	return resp
}

// dnsWarnState throttles the transport's warnings: a private zone that stops
// resolving is queried by every consumer on the host (an observed retry storm
// ran at ~24 queries/second), so an unthrottled WARN would flood the log
// exactly the way the silent failure floods the monitor database.
var dnsWarnState struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

// warnNetbirdDNS reports a failed custom-domain lookup: first failure
// immediately, then at most one line per netbirdDNSWarnInterval with a count of
// what was rolled up in between. addr is the address that was dialled, or ""
// when the failure happened before an address was available.
func warnNetbirdDNS(request *mdns.Msg, addr string, reason string) {
	name := "<unknown>"
	if request != nil && len(request.Question) > 0 {
		name = request.Question[0].Name
	}
	if addr == "" {
		addr = "<unset>"
	}
	now := time.Now()
	dnsWarnState.mu.Lock()
	if !dnsWarnState.last.IsZero() && now.Sub(dnsWarnState.last) < netbirdDNSWarnInterval {
		dnsWarnState.suppressed++
		dnsWarnState.mu.Unlock()
		return
	}
	suppressed := dnsWarnState.suppressed
	dnsWarnState.suppressed = 0
	dnsWarnState.last = now
	dnsWarnState.mu.Unlock()
	if suppressed > 0 {
		log.Warn(fmt.Sprintf("netbird DNS: %s via %s failed: %s (%d similar failures suppressed)", name, addr, reason, suppressed))
		return
	}
	log.Warn(fmt.Sprintf("netbird DNS: %s via %s failed: %s", name, addr, reason))
}
