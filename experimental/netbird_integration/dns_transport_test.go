//go:build with_netbird

package netbird_integration

import (
	"context"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/sagernet/sing-box/option"
)

// newTestTransport builds the transport the way the DNS registry does.
func newTestTransport(t *testing.T) *Transport {
	t.Helper()
	transport, err := NewTransport(context.Background(), nil, DNSTransportType, option.LocalDNSServerOptions{})
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	typed, ok := transport.(*Transport)
	if !ok {
		t.Fatalf("NewTransport returned %T, want *Transport", transport)
	}
	return typed
}

// TestExchangeFailsClosedWithoutEngine pins the contract that a custom-zone
// query is never answered from a public resolver.
//
// Only overlay-private names reach this transport (the injected config selects
// it by rule-set and never makes it the final resolver), so a public resolver
// cannot have the answer: falling back to one turned a local failure into a
// cacheable public NXDOMAIN, and because sing-box's own TUN recaptures
// host-originated DNS and hijacks it, the fallback packet was matched by the
// same rule-set and re-entered this transport — recursing with no base case.
// With no engine there is nothing to ask, so the lookup must fail in-band as
// SERVFAIL, immediately and without leaving the process.
func TestExchangeFailsClosedWithoutEngine(t *testing.T) {
	previousKernel := IsKernelMode()
	previousAddr := GetDNSAddr()
	previousClient := GetClient()
	SetKernelMode(false) // netstack mode: no system state involved
	SetDNSAddr("")
	ClearClient()
	t.Cleanup(func() {
		SetKernelMode(previousKernel)
		SetDNSAddr(previousAddr)
		SetClient(previousClient)
	})

	request := new(mdns.Msg)
	request.SetQuestion("host.example.com.", mdns.TypeA)

	response, err := newTestTransport(t).Exchange(context.Background(), request)
	if err != nil {
		t.Fatalf("Exchange returned a transport error, want an in-band answer: %v", err)
	}
	if response == nil {
		t.Fatal("Exchange returned no response")
	}
	if response.Rcode != mdns.RcodeServerFailure {
		t.Fatalf("Rcode = %s, want SERVFAIL", mdns.RcodeToString[response.Rcode])
	}
	if len(response.Answer) != 0 {
		t.Fatalf("SERVFAIL response carries answers: %+v", response.Answer)
	}
	if len(response.Question) != 1 || response.Question[0].Name != "host.example.com." {
		t.Fatalf("question not echoed: %+v", response.Question)
	}
}

// TestEnsureNetbirdDNSAddrNetstack covers the netstack half of the address
// resolution: the engine computes the address from the overlay CIDR when it
// syncs, so an unset address means "not synced yet" and must surface as an
// error instead of being dialled as an empty string.
func TestEnsureNetbirdDNSAddrNetstack(t *testing.T) {
	previousKernel := IsKernelMode()
	previousAddr := GetDNSAddr()
	SetKernelMode(false)
	SetDNSAddr("")
	t.Cleanup(func() {
		SetKernelMode(previousKernel)
		SetDNSAddr(previousAddr)
	})

	if _, err := ensureNetbirdDNSAddr(); err == nil {
		t.Fatal("expected an error before any sync recorded an address")
	}

	SetDNSAddr("192.0.2.1:53")
	addr, err := ensureNetbirdDNSAddr()
	if err != nil {
		t.Fatalf("ensureNetbirdDNSAddr: %v", err)
	}
	if addr != "192.0.2.1:53" {
		t.Fatalf("addr = %q, want the recorded address", addr)
	}
}

// TestServFailResponseMatchesRequest checks the shape of the failure answer:
// same id, question echoed, no answers.
func TestServFailResponseMatchesRequest(t *testing.T) {
	request := new(mdns.Msg)
	request.SetQuestion("host.example.com.", mdns.TypeAAAA)
	request.Id = 0x1234

	response := servFailResponse(request)
	if response.Id != request.Id {
		t.Fatalf("Id = %d, want %d", response.Id, request.Id)
	}
	if response.Rcode != mdns.RcodeServerFailure {
		t.Fatalf("Rcode = %s, want SERVFAIL", mdns.RcodeToString[response.Rcode])
	}
	if !response.Response {
		t.Fatal("response flag not set")
	}
	if len(response.Question) != 1 || response.Question[0].Qtype != mdns.TypeAAAA {
		t.Fatalf("question not echoed: %+v", response.Question)
	}
}

// TestComputeDNSAddrEmptyCIDR documents that a missing overlay CIDR yields no
// address (each account has its own overlay network; guessing one is worse than
// reporting nothing).
func TestComputeDNSAddrEmptyCIDR(t *testing.T) {
	for _, cidr := range []string{"", "not-a-cidr", "2001:db8::/32", "192.0.2.1/32"} {
		if addr := ComputeDNSAddr(cidr); addr != "" {
			t.Fatalf("ComputeDNSAddr(%q) = %q, want \"\"", cidr, addr)
		}
	}
	if addr := ComputeDNSAddr("192.0.2.0/24"); addr != "192.0.2.254:53" {
		t.Fatalf("ComputeDNSAddr(192.0.2.0/24) = %q, want the last usable address", addr)
	}
}
