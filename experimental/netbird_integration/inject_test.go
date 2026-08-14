package netbird_integration

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

const testConfig = `{
  "dns": {"servers": [{"tag": "main", "address": "223.5.5.5"}], "rules": [{"domain_suffix": ["example.com"], "server": "main"}]},
  "outbounds": [{"type": "direct", "tag": "direct"}, {"type": "vless", "tag": "proxy"}],
  "route": {"rules": [
    {"rule_set": "geosite-geolocation-!cn", "outbound": "proxy"},
    {"rule_set": "geoip-cn", "outbound": "direct"}
  ]}
}`

func ruleList(t *testing.T, raw []byte) (route []any, dnsRules []any) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	route = cfg["route"].(map[string]any)["rules"].([]any)
	dnsRules = cfg["dns"].(map[string]any)["rules"].([]any)
	return
}

func countRouteRules(route []any, kind string) int {
	n := 0
	for _, r := range route {
		m := r.(map[string]any)
		switch kind {
		case "process":
			if _, ok := m["process_path"]; ok {
				n++
			}
		case "domain-direct":
			if m["outbound"] == "direct" {
				if _, ok := m["domain_suffix"]; ok {
					n++
				}
			}
		}
	}
	return n
}

func TestInjectBypassUserspace(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)

	out, err := InjectNetbirdJSON([]byte(testConfig), nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, out)

	// process_path bypass removed: embedded-bypass (socket-level
	// IP_UNICAST_IF/protect/fwmark) replaces the route-rule bypass.
	if n := countRouteRules(route, "process"); n != 0 {
		t.Fatalf("process_path rules = %d, want 0 (bypass removed)", n)
	}

	// domain bypass covers netbird.io + mgmt host
	foundDom := false
	for _, r := range route {
		m := r.(map[string]any)
		if m["outbound"] == "direct" {
			if ds, ok := m["domain_suffix"].([]any); ok {
				s := fmt.Sprint(ds)
				if strings.Contains(s, "netbird.io") && strings.Contains(s, "nb.example.wang") {
					foundDom = true
				}
			}
		}
	}
	if !foundDom {
		t.Fatal("no domain bypass injected")
	}

	// bypass must be prepended before the proxy rule (ip_cidr ctlIPs → direct
	// is the first injected bypass now that process_path was removed)
	first, ok := route[0].(map[string]any)
	if !ok || first["outbound"] != "direct" {
		t.Fatal("bypass rule not at the front of route.rules")
	}
	if _, hasCIDR := first["ip_cidr"]; !hasCIDR {
		if _, hasDom := first["domain_suffix"]; !hasDom {
			t.Fatal("front bypass rule is neither ip_cidr nor domain_suffix")
		}
	}

	// dns-direct rule present
	_, dnsR := ruleList(t, out)
	foundDNS := false
	for _, r := range dnsR {
		if r.(map[string]any)["server"] == "dns-direct" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Fatal("no dns-direct rule injected")
	}

	// userspace: nb-out outbound + overlay CIDR rule
	var cfg map[string]any
	json.Unmarshal(out, &cfg)
	foundNB := false
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "nb-out" {
			foundNB = true
		}
	}
	if !foundNB {
		t.Fatal("nb-out not injected in userspace mode")
	}
	foundOverlay := false
	for _, r := range route {
		if cidrs, ok := r.(map[string]any)["ip_cidr"].([]any); ok {
			for _, c := range cidrs {
				if fmt.Sprint(c) == "100.121.0.0/16" {
					foundOverlay = true
				}
			}
		}
	}
	if !foundOverlay {
		t.Fatal("overlay CIDR rule missing in userspace mode")
	}
}

func TestInjectKernelMode(t *testing.T) {
	SetKernelMode(true)
	defer SetKernelMode(false)

	out, err := InjectNetbirdJSON([]byte(testConfig), []string{"svc.example.net"}, "100.121.0.0/16", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, out)

	// process_path bypass removed: kernel mode uses fwmark-based socket
	// bypass (control-plane marked 0x1BD00 → main), no route rule needed.
	if countRouteRules(route, "process") != 0 {
		t.Fatal("kernel mode: process bypass must not be injected (removed)")
	}
	// overlay route rule must NOT be injected (kernel route handles it)
	for _, r := range route {
		if cidrs, ok := r.(map[string]any)["ip_cidr"].([]any); ok {
			for _, c := range cidrs {
				if fmt.Sprint(c) == "100.121.0.0/16" {
					t.Fatal("kernel mode: overlay rule must not be injected")
				}
			}
		}
	}
	// nb-out must NOT be injected
	var cfg map[string]any
	json.Unmarshal(out, &cfg)
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "nb-out" {
			t.Fatal("kernel mode: nb-out must not be injected")
		}
	}
	// custom domain DNS rule still injected
	_, dnsR := ruleList(t, out)
	foundCustom := false
	for _, r := range dnsR {
		m := r.(map[string]any)
		if m["server"] == "nb" && strings.Contains(fmt.Sprint(m["domain_suffix"]), "svc.example.net") {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatal("kernel mode: custom domain DNS rule missing")
	}
	// mgmtURL empty → dns-direct rule covers only netbird.io
	foundDNS := false
	for _, r := range dnsR {
		m := r.(map[string]any)
		if m["server"] == "dns-direct" {
			ds := m["domain_suffix"].([]any)
			if len(ds) == 1 && fmt.Sprint(ds[0]) == "netbird.io" {
				foundDNS = true
			}
		}
	}
	if !foundDNS {
		t.Fatal("kernel mode: dns-direct rule missing or wrong domains")
	}
}

func TestInjectIdempotent(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)

	first, err := InjectNetbirdJSON([]byte(testConfig), nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InjectNetbirdJSON(first, nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, second)
	// process_path bypass was removed: embedded-bypass (socket-level
	// IP_UNICAST_IF/protect/fwmark) makes it dead weight.
	if n := countRouteRules(route, "process"); n != 0 {
		t.Fatalf("process rules after double inject = %d, want 0 (process_path bypass removed)", n)
	}
	if n := countRouteRules(route, "domain-direct"); n != 1 {
		t.Fatalf("domain-direct rules after double inject = %d, want 1", n)
	}
	_, dnsR := ruleList(t, second)
	dnsCount := 0
	for _, r := range dnsR {
		if r.(map[string]any)["server"] == "dns-direct" {
			dnsCount++
		}
	}
	if dnsCount != 1 {
		t.Fatalf("dns-direct rules after double inject = %d, want 1", dnsCount)
	}
	var cfg map[string]any
	json.Unmarshal(second, &cfg)
	nbCount := 0
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "nb-out" {
			nbCount++
		}
	}
	if nbCount != 1 {
		t.Fatalf("nb-out after double inject = %d, want 1", nbCount)
	}
}

func TestInjectExistingManualRulesNotDuplicated(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)
	exe, _ := os.Executable()
	exeJSON, _ := json.Marshal(exe)

	cfg := `{
	  "route": {"rules": [
	    {"process_path": [` + string(exeJSON) + `], "outbound": "direct"},
	    {"domain_suffix": ["netbird.io", "nb.example.wang"], "outbound": "direct"},
	    {"rule_set": "geosite-geolocation-!cn", "outbound": "proxy"}
	  ]},
	  "dns": {"rules": [{"domain_suffix": ["netbird.io", "nb.example.wang"], "server": "dns-direct"}]}
	}`
	out, err := InjectNetbirdJSON([]byte(cfg), nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, out)
	if n := countRouteRules(route, "process"); n != 1 {
		t.Fatalf("manual process rule duplicated: %d", n)
	}
	if n := countRouteRules(route, "domain-direct"); n != 1 {
		t.Fatalf("manual domain rule duplicated: %d", n)
	}
	_, dnsR := ruleList(t, out)
	dnsCount := 0
	for _, r := range dnsR {
		if r.(map[string]any)["server"] == "dns-direct" {
			dnsCount++
		}
	}
	if dnsCount != 1 {
		t.Fatalf("manual dns-direct duplicated: %d", dnsCount)
	}
}

func TestInjectStaleOverlayCleaned(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)

	cfg := `{"route": {"rules": [
	  {"ip_cidr": ["100.121.0.0/16"], "outbound": "nb-out"},
	  {"rule_set": "geosite-!cn", "outbound": "proxy"}
	]}, "dns": {}}`
	out, err := InjectNetbirdJSON([]byte(cfg), nil, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, out)
	count := 0
	for _, r := range route {
		m := r.(map[string]any)
		if cidrs, ok := m["ip_cidr"].([]any); ok {
			count++
			for _, c := range cidrs {
				if fmt.Sprint(c) == "100.121.0.0/16" && m["outbound"] != "nb-out" {
					t.Fatal("stale overlay rule not cleaned")
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("ip_cidr rules = %d, want 1 (stale cleaned, fresh injected)", count)
	}
}

func TestInjectControlPlaneIPs(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)
	ctlIPs := []string{"47.120.70.32/32", "1.2.3.4/32"}

	out, err := InjectNetbirdJSON([]byte(testConfig), nil, "", "https://nb.example.wang", ctlIPs)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := ruleList(t, out)

	found := false
	for _, r := range route {
		m := r.(map[string]any)
		if m["outbound"] == "direct" {
			if cidrs, ok := m["ip_cidr"].([]any); ok {
				s := fmt.Sprint(cidrs)
				if s == fmt.Sprint([]any{"47.120.70.32/32", "1.2.3.4/32"}) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("no control-plane ip_cidr → direct rule injected")
	}

	// Idempotent: same IPs must not duplicate the rule.
	second, err := InjectNetbirdJSON(out, nil, "", "https://nb.example.wang", ctlIPs)
	if err != nil {
		t.Fatal(err)
	}
	route2, _ := ruleList(t, second)
	count := 0
	for _, r := range route2 {
		m := r.(map[string]any)
		if cidrs, ok := m["ip_cidr"].([]any); ok {
			if fmt.Sprint(cidrs) == fmt.Sprint([]any{"47.120.70.32/32", "1.2.3.4/32"}) {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("control-plane ip_cidr rules = %d, want 1 (idempotent)", count)
	}
}

func TestInjectDNSFallback(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)

	// Profile with dns-remote server + geosite-geolocation-!cn rule-set:
	// final → nb (custom-domain fallback), non-CN domains stay on dns-remote.
	cfg := `{
	  "dns": {"servers": [
	    {"type": "https", "tag": "dns-direct", "server": "223.5.5.5"},
	    {"type": "https", "tag": "dns-remote", "server": "1.1.1.1"}
	  ], "final": "dns-remote"},
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rule_set": [{"tag": "geosite-geolocation-!cn", "type": "remote"}], "rules": []}
	}`
	out, err := InjectNetbirdJSON([]byte(cfg), nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(out, &parsed)
	dns := parsed["dns"].(map[string]any)
	if fmt.Sprint(dns["final"]) != "nb" {
		t.Fatalf("final = %v, want nb", dns["final"])
	}
	count := 0
	for _, r := range dns["rules"].([]any) {
		m := r.(map[string]any)
		if m["server"] == "dns-remote" && m["rule_set"] == "geosite-geolocation-!cn" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("geosite-!cn → dns-remote rules = %d, want 1", count)
	}

	// Idempotent: second inject must not duplicate the rule_set rule.
	second, err := InjectNetbirdJSON(out, nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed2 map[string]any
	json.Unmarshal(second, &parsed2)
	count2 := 0
	for _, r := range parsed2["dns"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		if m["server"] == "dns-remote" && m["rule_set"] == "geosite-geolocation-!cn" {
			count2++
		}
	}
	if count2 != 1 {
		t.Fatalf("geosite-!cn → dns-remote after double inject = %d, want 1", count2)
	}
}

func TestInjectDNSFallbackMinimalProfile(t *testing.T) {
	SetKernelMode(false)
	defer SetKernelMode(false)

	// testConfig lacks a dns-remote server and a declared rule-set:
	// final still becomes nb, but no geosite-!cn rule may be injected
	// (referencing a missing server/rule-set would break sing-box startup).
	out, err := InjectNetbirdJSON([]byte(testConfig), nil, "", "https://nb.example.wang", nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(out, &parsed)
	dns := parsed["dns"].(map[string]any)
	if fmt.Sprint(dns["final"]) != "nb" {
		t.Fatalf("final = %v, want nb", dns["final"])
	}
	for _, r := range dns["rules"].([]any) {
		m := r.(map[string]any)
		if m["rule_set"] == "geosite-geolocation-!cn" {
			t.Fatal("geosite-!cn rule injected without dns-remote server or rule-set declaration")
		}
	}
}
