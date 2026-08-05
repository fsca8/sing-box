//go:build with_netbird

package netbird_integration

import (
	"encoding/json"
	"strings"
	"testing"

	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
)

func TestExtractNetworkCIDR(t *testing.T) {
	// 组件式 wire format: 精确 net_cidr
	resp := &mgmProto.SyncResponse{
		NetworkMapEnvelope: &mgmProto.NetworkMapEnvelope{
			Payload: &mgmProto.NetworkMapEnvelope_Full{
				Full: &mgmProto.NetworkMapComponentsFull{
					Network: &mgmProto.AccountNetwork{NetCidr: "100.90.0.0/16"},
				},
			},
		},
	}
	if got := ExtractNetworkCIDR(resp); got != "100.90.0.0/16" {
		t.Fatalf("component path: got %q, want 100.90.0.0/16", got)
	}

	// legacy 路径: 对端地址按 /16 掩码回退
	resp2 := &mgmProto.SyncResponse{
		NetworkMap: &mgmProto.NetworkMap{
			PeerConfig: &mgmProto.PeerConfig{Address: "100.121.0.2"},
		},
	}
	if got := ExtractNetworkCIDR(resp2); got != "100.121.0.0/16" {
		t.Fatalf("legacy path: got %q, want 100.121.0.0/16", got)
	}

	// 非默认前缀的对端地址也正确掩码
	resp3 := &mgmProto.SyncResponse{
		NetworkMap: &mgmProto.NetworkMap{
			PeerConfig: &mgmProto.PeerConfig{Address: "100.64.33.100"},
		},
	}
	if got := ExtractNetworkCIDR(resp3); got != "100.64.0.0/16" {
		t.Fatalf("legacy mask: got %q, want 100.64.0.0/16", got)
	}

	// nil / 空
	if got := ExtractNetworkCIDR(nil); got != "" {
		t.Fatalf("nil: got %q", got)
	}
}

func TestInjectNetbirdJSONNetworkCIDR(t *testing.T) {
	raw := []byte(`{"outbounds":[{"type":"direct","tag":"direct"}],"route":{"rules":[]}}`)

	// 动态网段生效, 且规则插在最前
	out, err := InjectNetbirdJSON(raw, nil, "100.90.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	rules, _ := m["route"].(map[string]any)["rules"].([]any)
	first, _ := rules[0].(map[string]any)
	cidrs, _ := first["ip_cidr"].([]any)
	if len(cidrs) != 1 || cidrs[0] != "100.90.0.0/16" {
		t.Fatalf("dynamic cidr not applied: %v", cidrs)
	}
	if first["outbound"] != "nb-out" {
		t.Fatalf("outbound not nb-out: %v", first["outbound"])
	}

	// 空网段回退默认
	out2, err := InjectNetbirdJSON(raw, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out2), "100.121.0.0/16") {
		t.Fatalf("default fallback missing: %s", out2)
	}
}
