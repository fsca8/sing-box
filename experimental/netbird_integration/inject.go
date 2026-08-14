// No build tags — pure JSON manipulation, accessible in all builds.
package netbird_integration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// kernelMode selects the Linux kernel-TUN data path (Route A):
//   - netbird creates a real kernel TUN (wt0) instead of the userspace netstack
//   - a single static kernel route sends the overlay CIDR straight to wt0,
//     bypassing the sing-box TUN stack (no double TCP termination)
//   - sing-box config injection skips the overlay route rules (kernel handles
//     the overlay), keeping the custom-domain DNS rules and the engine bypass
//
// Set by StartAll() before the engine starts. Only ever true on Linux.
var kernelMode bool

// IsKernelMode reports whether the kernel-TUN data path is active.
func IsKernelMode() bool { return kernelMode }

// SetKernelMode records whether the kernel-TUN data path is active.
func SetKernelMode(v bool) { kernelMode = v }

// InjectNetbirdJSON adds netbird DNS server, outbound, route/rule-set entries,
// and the engine-traffic bypass to the raw sing-box config. It returns the
// modified JSON bytes.
//
// customDomains (from netbird SyncResponse) get domain-specific route and DNS
// rules pointing to the netbird outbound/DNS server so they resolve through
// the netbird tunnel.
// networkCIDR (from netbird SyncResponse, e.g. "100.121.0.0/16") is the
// account's overlay subnet and is always routed through netbird; empty falls
// back to the netbird default 100.121.0.0/16.
// mgmtURL is the netbird management URL (netbird-config.json management_url);
// its host — together with netbird.io — feeds the engine-traffic bypass rules.
// ctlIPs are the engine's control-plane server IPs (management/STUN/TURN/
// relay hosts, resolved from mgmtURL by the caller). They get an explicit
// ip_cidr → direct rule: remote rule-sets (geoip/geosite) are downloaded
// asynchronously and may not be loaded when the engine performs its first
// STUN probes at startup — without an explicit IP rule those probes fall
// through to `final` (usually the proxy) and the srflx candidate comes back
// poisoned with the proxy exit IP, breaking P2P until a restart.
//
// Engine-traffic bypass (BOTH kernel-TUN and userspace paths): the netbird
// engine's own sockets (STUN / management / relay / TURN / wg probes) are
// ordinary sockets, so auto_route captures them into the sing-box TUN like any
// app traffic. Without a bypass they hit `geosite-!cn → proxy` and their srflx
// candidates get poisoned with the proxy exit IP → ICE hole-punch fails → the
// tunnel silently falls back to the TURN relay (~350ms vs ~19ms direct).
// Note: kernel-TUN only kernel-routes the overlay CIDR to wt0; the engine's
// control traffic still goes through the sing-box TUN, so the bypass is
// required there too. Injected rules:
//   route: process_path [current executable] → direct  (matches raw-IP conns
//          such as STUN probes; the essential one)
//          domain_suffix [netbird.io, mgmt-host] → direct (SNI-carrying conns;
//          also the only reliable match on Android, where process-path lookup
//          resolves to the app process, not the embedded lib)
//   dns:   domain_suffix [netbird.io, mgmt-host] → dns-direct (control-plane
//          DNS must not go through dns-remote → proxy: 200-430ms → 58ms)
// All injections are idempotent: matching rules already present in the config
// (e.g. hand-edited) are kept and not duplicated.
func InjectNetbirdJSON(rawData []byte, customDomains []string, networkCIDR string, mgmtURL string, ctlIPs []string) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(rawData, &raw); err != nil {
		return nil, err
	}

	// Inject DNS server — netbird as DNS transport (domain-specific rules below)
	dnsSection, _ := raw["dns"].(map[string]any)
	if dnsSection == nil {
		dnsSection = make(map[string]any)
		raw["dns"] = dnsSection
	}
	servers, _ := dnsSection["servers"].([]any)
	if !hasServerTag(servers, "nb") {
		servers = append(servers, map[string]any{"type": "netbird", "tag": "nb"})
		dnsSection["servers"] = servers
	}
	// Don't set final="nb" — only route custom domains through netbird.

	routeSection, _ := raw["route"].(map[string]any)
	if routeSection == nil {
		routeSection = make(map[string]any)
		raw["route"] = routeSection
	}
	routeRules, _ := routeSection["rules"].([]any)
	// Drop stale hand-edited overlay rules (from earlier config edits)
	var cleaned []any
	for _, r := range routeRules {
		rule, ok := r.(map[string]any)
		skip := false
		if ok {
			if cidrs, ok := rule["ip_cidr"].([]any); ok {
				for _, c := range cidrs {
					if fmt.Sprint(c) == "100.121.0.0/16" {
						skip = true
						break
					}
				}
			}
		}
		if !skip {
			cleaned = append(cleaned, r)
		}
	}

	// Control-plane domains: netbird.io (built-in default STUN + default mgmt)
	// plus the configured management host (self-hosted deployments).
	ctlDomains := []string{"netbird.io"}
	if mgmtURL != "" {
		if u, err := url.Parse(mgmtURL); err == nil && u.Hostname() != "" {
			ctlDomains = append(ctlDomains, u.Hostname())
		}
	}

	// Engine-traffic bypass — prepended so it always wins over proxy rules.
	prepended := []any{}
	// Control-plane IPs → direct. Must not depend on geoip/geosite rule-sets:
	// they load asynchronously and are often not ready when the engine does
	// its first STUN probes (StartAll runs before sing-box is created).
	if len(ctlIPs) > 0 && !hasIPDirectRule(cleaned, ctlIPs) {
		prepended = append(prepended, map[string]any{
			"ip_cidr":  ctlIPs,
			"outbound": "direct",
		})
	}
	if !hasDomainDirectRule(cleaned, ctlDomains) {
		prepended = append(prepended, map[string]any{
			"domain_suffix": ctlDomains,
			"outbound":      "direct",
		})
	}

	// Control-plane DNS direct (dns-direct is a built-in sing-box server).
	if !hasDNSDirectRule(dnsSection, ctlDomains) {
		dnsRules, _ := dnsSection["rules"].([]any)
		dnsSection["rules"] = append(dnsRules, map[string]any{
			"domain_suffix": ctlDomains,
			"server":        "dns-direct",
		})
	}

	// Netbird outbound + overlay route rules only in userspace (netstack) mode.
	// In kernel-TUN mode the kernel routes the overlay CIDR directly to wt0;
	// injecting route rules would pull overlay traffic into the sing-box TUN
	// stack and recreate the double-TCP overhead. (The engine bypass above is
	// still injected in kernel mode — the engine's control traffic is not
	// overlay traffic and does go through the sing-box TUN.)
	if !IsKernelMode() {
		outbounds, _ := raw["outbounds"].([]any)
		if !hasServerTag(outbounds, "nb-out") {
			outbounds = append(outbounds, map[string]any{"type": "netbird", "tag": "nb-out"})
			raw["outbounds"] = outbounds
		}

		// Netbird internal IP range — always route through netbird outbound
		// (dynamic from sync; falls back to the netbird default /16)
		nbCIDR := networkCIDR
		if nbCIDR == "" {
			nbCIDR = "100.121.0.0/16"
		}
		prepended = append(prepended, map[string]any{
			"ip_cidr":  []string{nbCIDR},
			"outbound": "nb-out",
		})
		// Domain-specific route rules for each custom domain
		for _, d := range customDomains {
			prepended = append(prepended, map[string]any{
				"domain_suffix": strings.TrimSuffix(d, "."),
				"outbound":      "nb-out",
			})
		}
	}
	routeSection["rules"] = append(prepended, cleaned...)

	// Domain-specific DNS rules for each custom domain
	for _, d := range customDomains {
		clean := strings.TrimSuffix(d, ".")
		rules, _ := dnsSection["rules"].([]any)
		rules = append(rules, map[string]any{
			"domain_suffix": clean,
			"server":        "nb",
		})
		dnsSection["rules"] = rules
	}

	// DNS 兜底: 未命中任何规则的域名(含 netbird 自定义域名)走 nb transport。
	// nb transport 对未知域名返回 Refused 后自动 fallback 公网 223.5.5.5
	// (dns_transport.go), 引擎未运行时行为等价于 dns-direct; 引擎由恢复循环
	// 拉起后 SetClient 即复活 —— 自定义域名无需引擎启动时就注入规则。
	// 非 CN 域名保持走 dns-remote(经代理, 防 DNS 污染): 显式 rule_set 规则
	// 拦截, 避免被 final=nb 兜底吞掉。仅在 profile 具备 dns-remote server
	// 与 geosite-geolocation-!cn rule_set 时注入, 否则回退现状。
	if hasServerTag(servers, "dns-remote") && hasRuleSetTag(raw, "geosite-geolocation-!cn") &&
		!hasRuleSetServerRule(dnsSection, "geosite-geolocation-!cn", "dns-remote") {
		rules, _ := dnsSection["rules"].([]any)
		dnsSection["rules"] = append(rules, map[string]any{
			"rule_set": "geosite-geolocation-!cn",
			"server":   "dns-remote",
		})
	}
	if fmt.Sprint(dnsSection["final"]) != "nb" {
		dnsSection["final"] = "nb"
	}

	return json.Marshal(raw)
}

// hasServerTag reports whether the JSON array already contains an entry
// (DNS server or outbound) with the given tag.
func hasServerTag(items []any, tag string) bool {
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(m["tag"]) == tag {
			return true
		}
	}
	return false
}

// hasIPDirectRule reports whether rules already contain an ip_cidr rule with
// outbound "direct" covering all of the given IPs.
func hasIPDirectRule(rules []any, ips []string) bool {
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(rule["outbound"]) != "direct" {
			continue
		}
		cidrs, ok := rule["ip_cidr"].([]any)
		if !ok {
			continue
		}
		if containsAll(cidrs, ips) {
			return true
		}
	}
	return false
}

// hasDomainDirectRule reports whether rules already contain a domain_suffix
// rule covering all of domains with outbound "direct".
func hasDomainDirectRule(rules []any, domains []string) bool {
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(rule["outbound"]) != "direct" {
			continue
		}
		ds, ok := rule["domain_suffix"].([]any)
		if !ok {
			continue
		}
		if containsAll(ds, domains) {
			return true
		}
	}
	return false
}

// hasDNSDirectRule reports whether dns.rules already route all of domains to
// the built-in dns-direct server.
func hasDNSDirectRule(dnsSection map[string]any, domains []string) bool {
	rules, _ := dnsSection["rules"].([]any)
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(rule["server"]) != "dns-direct" {
			continue
		}
		ds, ok := rule["domain_suffix"].([]any)
		if !ok {
			continue
		}
		if containsAll(ds, domains) {
			return true
		}
	}
	return false
}

// hasRuleSetTag reports whether route.rule_set contains a rule-set with the
// given tag (DNS rules can reference rule-sets only when they are declared).
func hasRuleSetTag(raw map[string]any, tag string) bool {
	routeSection, _ := raw["route"].(map[string]any)
	ruleSets, _ := routeSection["rule_set"].([]any)
	for _, rs := range ruleSets {
		m, ok := rs.(map[string]any)
		if ok && fmt.Sprint(m["tag"]) == tag {
			return true
		}
	}
	return false
}

// hasRuleSetServerRule reports whether dns.rules already contains a rule_set
// rule routing the given rule-set to the given server tag.
func hasRuleSetServerRule(dnsSection map[string]any, ruleSet string, server string) bool {
	rules, _ := dnsSection["rules"].([]any)
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(rule["server"]) == server && fmt.Sprint(rule["rule_set"]) == ruleSet {
			return true
		}
	}
	return false
}

func containsAll(haystack []any, needles []string) bool {
	has := func(s string) bool {
		for _, h := range haystack {
			if fmt.Sprint(h) == s {
				return true
			}
		}
		return false
	}
	for _, n := range needles {
		if !has(n) {
			return false
		}
	}
	return true
}
