//go:build with_netbird

package netbird_integration

import (
	"context"
	"sync"

	mdns "github.com/miekg/dns"
	nbembed "github.com/netbirdio/netbird/client/embed"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

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
// Custom domains (from management server) return internal IPs; other domains
// cause sing-box to fall back to the next configured DNS server.
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

// Exchange resolves a DNS query through netbird's handler chain.
func (t *Transport) Exchange(ctx context.Context, message *mdns.Msg) (*mdns.Msg, error) {
	client := GetClient()
	if client == nil {
		return nil, context.Canceled
	}
	return client.ResolveDNS(ctx, message)
}

// ExchangeAsync wraps Exchange in a goroutine.
func (t *Transport) ExchangeAsync(ctx context.Context, message *mdns.Msg, callback func(response *mdns.Msg, err error)) {
	go func() {
		resp, err := t.Exchange(ctx, message)
		callback(resp, err)
	}()
}
