//go:build with_netbird

package netbird_integration

import (
	"context"
	"net"
	"sync"
	"time"

	mdns "github.com/miekg/dns"
	nbembed "github.com/netbirdio/netbird/client/embed"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

// netbirdDNSServer is the DNS server address reachable through netbird's tunnel.
// Netbird peers are on 100.64.0.0/10; we use a standard DNS server through the tunnel.
const netbirdDNSServer = "100.100.100.100:53"

// globalClient stores the netbird embed client reference for DNS resolution.
// Set by SetClient() after embed Start(), read by Transport.Exchange().
var globalClient struct {
	mu     sync.Mutex
	client *nbembed.Client
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

// DNSTransportType is the sing-box DNS transport type used in config.
const DNSTransportType = "netbird"

// RegisterTransport registers the netbird DNS transport with sing-box's DNS
// transport registry. Called from an init() in include/ under with_netbird tag.
func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.LocalDNSServerOptions](registry, DNSTransportType, NewTransport)
}

var _ adapter.DNSTransport = (*Transport)(nil)

// Transport resolves DNS queries through netbird's engine DNS handler chain.
// Custom domains (from management server) return internal IPs.
// For non-custom domains, falls back to a public DNS server.
type Transport struct {
	tag     string
	started bool
	closeCh chan struct{}
	client  *mdns.Client
}

func NewTransport(ctx context.Context, logger log.ContextLogger, tag string, options option.LocalDNSServerOptions) (adapter.DNSTransport, error) {
	return &Transport{
		tag:     tag,
		closeCh: make(chan struct{}),
		client:  &mdns.Client{Net: "udp", Timeout: 5 * time.Second, SingleInflight: true},
	}, nil
}

func (t *Transport) Type() string           { return DNSTransportType }
func (t *Transport) Tag() string            { return t.tag }
func (t *Transport) Dependencies() []string { return nil }

func (t *Transport) Start(stage adapter.StartStage) error {
	t.started = true
	return nil
}

func (t *Transport) Close() error {
	close(t.closeCh)
	t.started = false
	return nil
}

func (t *Transport) Reset() {}

// Exchange resolves a DNS query through netbird's handler chain first.
// If netbird cannot resolve (Refused), falls back to public DNS.
func (t *Transport) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	client := GetClient()
	if client != nil {
		// Try resolving through netbird's tunnel DNS
		resp, err := t.resolveViaNetbird(ctx, client, message)
		if err == nil && resp != nil && resp.Rcode != mdns.RcodeRefused {
			return resp, nil
		}
	}
	// Fallback: public DNS via UDP
	return t.fallbackExchange(ctx, message)
}

// ExchangeAsync wraps Exchange in a goroutine.
func (t *Transport) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(response *mdns.Msg, err error)) {
	go func() {
		resp, err := t.Exchange(ctx, message)
		callback(resp, err)
	}()
}

// resolveViaNetbird sends the DNS query through the netbird tunnel using the client's DialContext.
func (t *Transport) resolveViaNetbird(ctx context.Context, client *nbembed.Client, message *mdns.Msg) (*mdns.Msg, error) {
	// Try netbird's internal DNS server first (100.x.x.x range)
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", netbirdDNSServer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	co := &mdns.Conn{Conn: conn}
	defer co.Close()
	if err := co.WriteMsg(message); err != nil {
		return nil, err
	}
	return co.ReadMsg()
}

// fallbackServer is the default public DNS server used when netbird can't resolve.
const fallbackServer = "223.5.5.5:53"

func (t *Transport) fallbackExchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", fallbackServer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	co := &mdns.Conn{Conn: conn}
	defer co.Close()
	if err := co.WriteMsg(message); err != nil {
		return nil, err
	}
	return co.ReadMsg()
}
