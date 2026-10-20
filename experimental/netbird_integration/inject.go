// No build tags — pure JSON manipulation, accessible in all builds.
package netbird_integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
)

// localDNSUnavailable reports whether `type: local` cannot work on this build's
// platform. Android has no /etc/resolv.conf: sing-box's `local` server only
// works when the embedding app supplies a platform transport via
// PlatformInterface.LocalDNSTransport() (libbox/config.go). singbird's
// VpnService returns null there, so `local` dials the Go default resolver and
// every query dies with "read udp [::1]:<port>->[::1]:53: connection refused".
// Kept as a var so tests can exercise the Android behaviour on any host.
var localDNSUnavailable = runtime.GOOS == "android"

// kernelMode selects the Linux kernel-TUN data path (Route A):
//   - netbird creates a real kernel TUN (wt0) instead of the userspace netstack
//   - a single static kernel route sends the overlay CIDR straight to wt0,
//     bypassing the sing-box TUN stack (no double TCP termination)
//   - sing-box config injection skips the overlay route rules (kernel handles
//     the overlay), keeping the custom-domain DNS rules and the engine bypass
//
// Set by StartAll() before the engine starts, and re-derived by every
// Engine.Start() — Stop() clears it, and restarts (network change, recovery) do
// not go through StartAll. Only ever true on Linux.
var kernelMode bool

// IsKernelMode reports whether the kernel-TUN data path is active.
func IsKernelMode() bool { return kernelMode }

// SetKernelMode records whether the kernel-TUN data path is active.
func SetKernelMode(v bool) { kernelMode = v }

// 控制面 DNS 规则引用的解析器**不是**写死的标签：用户的 dns.servers 里叫
// 什么都有可能（dns-direct 只是常见约定，可能不存在、可能被改名、也可能被
// 指到一条走代理的传输）。解析器由 controlPlaneResolver 按结构挑选；只有
// 当 profile 里根本没有"不经代理"的解析器时，才用下面这个自有命名空间补一台
// 系统解析器，且只在 local 可用的平台上补。
const controlPlaneResolverTag = "nb-dns-direct"

const customRuleSetTag = "nb-domains"
const customCIDRRuleSetTag = "nb-cidr"

// controlPlaneResolver picks the tag of a DNS server that resolves WITHOUT the
// proxy — the control plane must be resolvable before the tunnel is up, and a
// proxied resolver would make the engine wait for the very proxy it is trying
// to establish.
//
// 判定只看结构，不看标签名：没有 detour，或 detour 指向一个 direct 类型的
// 出站。优先挑地址是 IP 字面量（或 type: local）的条目 —— 名字型地址（如
// doh.pub）还要先解析一次，往往正是依赖本机解析器的那条，最不该被选中。
// 平台不支持 local 时跳过 local 条目。没有可用条目返回 ""。
func controlPlaneResolver(raw map[string]any, servers []any) string {
	directOutbound := make(map[string]bool)
	if outbounds, ok := raw["outbounds"].([]any); ok {
		for _, o := range outbounds {
			if m, ok := o.(map[string]any); ok && str(m["type"]) == "direct" {
				directOutbound[str(m["tag"])] = true
			}
		}
	}
	directCapable := func(m map[string]any) bool {
		tag := str(m["tag"])
		switch {
		case tag == "", tag == "nb", tag == "nb-out":
			return false
		case str(m["type"]) == "netbird":
			return false
		case str(m["type"]) == "local" && localDNSUnavailable:
			return false
		}
		detour := str(m["detour"])
		return detour == "" || directOutbound[detour]
	}
	// 第一轮：地址无需再解析的条目（IP 字面量 / 系统解析器）。
	for _, s := range servers {
		m, ok := s.(map[string]any)
		if !ok || !directCapable(m) {
			continue
		}
		if str(m["type"]) == "local" || isIPLiteral(dnsServerHost(m)) {
			return str(m["tag"])
		}
	}
	// 第二轮：其余直连条目（名字型地址，可能还要解析一次）。
	for _, s := range servers {
		if m, ok := s.(map[string]any); ok && directCapable(m) {
			return str(m["tag"])
		}
	}
	return ""
}

// InjectNetbirdJSON adds netbird DNS server, outbound, route/rule-set entries,
// and the engine-traffic bypass to the raw sing-box config. It returns the
// modified JSON bytes.
//
// Custom domains are NOT baked into per-domain rules: the config declares two
// local rule-sets and route/DNS rules reference them:
//   - nb-domains (file at ruleSetPath): domain_suffix list, referenced by the
//     DNS rule (server nb) and the route rule (outbound nb-out)
//   - nb-cidr (file at cidrRuleSetPath): the overlay CIDR, referenced by the
//     route rule only (DNS rules must not reference IP-bearing sets)
//
// The integration rewrites these files whenever the engine syncs; sing-box
// reloads the rule-sets at runtime (fswatch), so domains/CIDR arriving after
// startup (engine recovery) take effect without reloading the service. Both
// files must exist when the config loads (StartAll writes them, possibly
// empty/default, before returning). Empty ruleSetPath skips the rule-set
// machinery entirely (no custom-domain rules injected).
//
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
//
//	route: process_path [current executable] → direct  (matches raw-IP conns
//	       such as STUN probes; the essential one)
//	       domain_suffix [netbird.io, mgmt-host] → direct (SNI-carrying conns;
//	       also the only reliable match on Android, where process-path lookup
//	       resolves to the app process, not the embedded lib)
//	dns:   domain_suffix [netbird.io, mgmt-host] → dns-direct (control-plane
//	       DNS must not go through dns-remote → proxy: 200-430ms → 58ms)
//
// All injections are idempotent: matching rules already present in the config
// (e.g. hand-edited) are kept and not duplicated.
func InjectNetbirdJSON(rawData []byte, mgmtURL string, ctlIPs []string, ruleSetPath string, cidrRuleSetPath string) ([]byte, error) {
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
	// Drop stale rules generated by the earlier static-CIDR scheme: 旧版本把
	// overlay 网段直接写进配置（ip_cidr + outbound nb-out），现在由 nb-cidr
	// 规则集承载。只删"本工具生成过的那条"（同时带 ip_cidr 且出站是本工具
	// 的 netbird 出站），不碰用户自己写的同网段分流/reject 规则。
	var cleaned []any
	for _, r := range routeRules {
		rule, ok := r.(map[string]any)
		skip := false
		if ok {
			if _, hasCIDR := rule["ip_cidr"].([]any); hasCIDR && fmt.Sprint(rule["outbound"]) == "nb-out" {
				skip = true
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

	// Control-plane resolver（按结构挑，见 controlPlaneResolver）。规则引用的
	// server 必须真实存在于 dns.servers，否则 dns/router.go 只记一条
	// "transport not found" 并跳过该规则，控制面域名会退回 dns.final（通常走
	// 代理：200-430ms 且查询外泄）。profile 里没有直连解析器时才补一台系统
	// 解析器 —— 用的是本工具自己的 tag，不占用户的标签，也不用猜用户怎么命名。
	ctlResolver := controlPlaneResolver(raw, servers)
	if ctlResolver == "" && !localDNSUnavailable {
		servers = append(servers, map[string]any{"type": "local", "tag": controlPlaneResolverTag})
		dnsSection["servers"] = servers
		ctlResolver = controlPlaneResolverTag
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

		// Custom domains → nb-out via the local domain rule-set (domains live
		// in the file, updated at runtime by the engine; see customRuleSetTag).
		// Idempotent: skip when an existing rule (cleaned) or a just-prepended
		// one already references the set.
		if ruleSetPath != "" && !hasRuleSetRule(prepended, customRuleSetTag) && !hasRuleSetRule(cleaned, customRuleSetTag) {
			prepended = append(prepended, map[string]any{
				"rule_set": customRuleSetTag,
				"outbound": "nb-out",
			})
		}
		// Overlay CIDR → nb-out via the local CIDR rule-set (real-time updates
		// for accounts whose overlay differs from the default /16). The overlay
		// routing used to be a static ip_cidr rule baked into the config — it
		// was removed: the CIDR now lives in nb-cidr.json (written at StartAll
		// with the persisted/default value, refreshed by the engine after sync),
		// so the route rule-set reference covers it with zero startup ordering
		// dependency.
		if cidrRuleSetPath != "" && !hasRuleSetRule(prepended, customCIDRRuleSetTag) && !hasRuleSetRule(cleaned, customCIDRRuleSetTag) {
			prepended = append(prepended, map[string]any{
				"rule_set": customCIDRRuleSetTag,
				"outbound": "nb-out",
			})
		}
		// nb-cidr rule-set declaration — userspace only (kernel mode routes
		// the overlay via the kernel route, no route rules at all).
		// format 必须显式: 1.14 的 ruleSetDefaultFormat 用 url.Parse 推断
		// 扩展名, Windows 盘符路径(C:\...)被当成 URL scheme → 推断失败 →
		// "missing format"(Android /data/... 路径正常)。显式 source 全平台稳。
		if cidrRuleSetPath != "" && !hasRuleSetDecl(routeSection, customCIDRRuleSetTag) {
			ruleSets, _ := routeSection["rule_set"].([]any)
			routeSection["rule_set"] = append(ruleSets, map[string]any{
				"type":   "local",
				"tag":    customCIDRRuleSetTag,
				"path":   cidrRuleSetPath,
				"format": "source",
			})
		}
	}
	routeSection["rules"] = append(prepended, cleaned...)

	// Local rule-set declaration (nb-domains): route AND DNS rules resolve
	// rule-set tags through the route.rule_set registry (box.go:
	// router.Initialize(routeOptions.RuleSet)). Declared in both modes — the
	// DNS custom-domain rule needs it in kernel mode too. format 显式, 理由
	// 同上(Windows 路径推断失败)。
	if ruleSetPath != "" && !hasRuleSetDecl(routeSection, customRuleSetTag) {
		ruleSets, _ := routeSection["rule_set"].([]any)
		routeSection["rule_set"] = append(ruleSets, map[string]any{
			"type":   "local",
			"tag":    customRuleSetTag,
			"path":   ruleSetPath,
			"format": "source",
		})
	}

	// —— 本工具的 DNS 规则一律**前置** ——
	// 两条规则原来是 append 到 dns.rules 末尾的，只要 profile 里存在一条无条件
	// 的终结规则（{"action":"route","server":X} / {"action":"reject"} / 早退的
	// catch-all），它们就永远不被求值；DNS 规则顺序错位没有任何告警，表现就是
	// 「自定义域名突然解析不出来、公网也没有它的 A 记录」。前置把这个隐含前提
	// 去掉：规则顺序由本工具负责，profile 怎么写都行。
	//
	// 顺序不能反 —— 控制面直连在前、nb 在后：自建管理域名常是某个自定义域名的
	// 子域（管理 nb.example.com、自定义 example.com），domain_suffix 会把管理
	// 域名一并匹配进 nb 规则，把控制面 DNS 送进它自己还没建好的隧道。
	//
	// 前置之前先摘掉旧布局/上次注入留下的同款规则：既避免重复，也避免旧的
	// 那条仍然躺在 catch-all 后面冒充"已注入"。
	dnsRules, _ := dnsSection["rules"].([]any)
	dnsRules = detachRule(dnsRules, func(m map[string]any) bool {
		return isControlPlaneDNSRule(m, ctlResolver, controlPlaneResolverTag)
	})
	if ruleSetPath != "" {
		dnsRules = detachRule(dnsRules, isNbDNSRule)
	}

	var dnsPrepended []any
	if ctlResolver != "" {
		dnsPrepended = append(dnsPrepended, map[string]any{
			"domain_suffix": ctlDomains,
			"server":        ctlResolver,
		})
	}
	// Custom domains → nb DNS transport (both kernel-TUN and userspace: the
	// kernel route handles overlay IPs, but domain resolution still needs the
	// tunnel DNS). References ONLY the domain rule-set (an IP-bearing set
	// referenced by a DNS rule is rejected by the 1.14 legacy-address-filter
	// check).
	if ruleSetPath != "" {
		dnsPrepended = append(dnsPrepended, map[string]any{
			"rule_set": customRuleSetTag,
			"server":   "nb",
		})
	}
	if len(dnsPrepended) > 0 {
		dnsSection["rules"] = append(dnsPrepended, dnsRules...)
	}

	// 注意: 不做 final=nb 兜底。兜底会让所有未命中规则的域名(非 CN 冷门
	// 域名、rule_set 异步加载期间的域名)先走隧道 DNS —— 引擎在但隧道
	// 不通时一次解析 5s 超时 + fallback 5s, 直接卡死。自定义域名靠上面的
	// nb-domains 显式规则(引擎 sync 成功后注入), 不影响其他域名。
	// final 保持用户 profile 原样(通常 dns-remote 走代理, 快)。

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

// hasRuleSetDecl reports whether the route.rule_set section already declares
// a rule-set with the given tag.
func hasRuleSetDecl(routeSection map[string]any, tag string) bool {
	ruleSets, _ := routeSection["rule_set"].([]any)
	for _, rs := range ruleSets {
		m, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(m["tag"]) == tag {
			return true
		}
	}
	return false
}

// hasRuleSetRule reports whether the rule list already contains a rule whose
// rule_set matcher references the given tag (route and DNS rules both use
// the same `rule_set` key; the value is a Listable string or array).
func hasRuleSetRule(rules []any, tag string) bool {
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		switch v := m["rule_set"].(type) {
		case string:
			if v == tag {
				return true
			}
		case []any:
			for _, s := range v {
				if fmt.Sprint(s) == tag {
					return true
				}
			}
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

// isControlPlaneDNSRule matches a rule with the same intent as the one this
// tool injects: {"domain_suffix": [...netbird.io...], "server": <a direct
// resolver>}. servers 传入本轮挑中的 tag 与本工具自有的 tag —— 这样既能摘掉
// 上一次注入的遗留（解析器 tag 可能因 profile 改动而变），也不会去动用户写
// 的、指向别的解析器的规则。键集合必须恰好是 domain_suffix + server。
func isControlPlaneDNSRule(m map[string]any, servers ...string) bool {
	if !hasExactKeys(m, "domain_suffix", "server") {
		return false
	}
	srv := str(m["server"])
	matched := false
	for _, s := range servers {
		if s != "" && srv == s {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	ds, ok := m["domain_suffix"].([]any)
	return ok && containsAll(ds, []string{"netbird.io"})
}

// isNbDNSRule matches the generated {"rule_set": "nb-domains", "server": "nb"}.
func isNbDNSRule(m map[string]any) bool {
	return hasExactKeys(m, "rule_set", "server") &&
		str(m["rule_set"]) == customRuleSetTag &&
		str(m["server"]) == "nb"
}

// detachRule returns rules minus the entries for which match reports true.
func detachRule(rules []any, match func(map[string]any) bool) []any {
	kept := make([]any, 0, len(rules))
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok && match(m) {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// hasExactKeys reports whether m contains exactly the given keys (no extra
// matchers, no extra actions).
func hasExactKeys(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// str renders a JSON value as a string. fmt.Sprint would turn a missing key
// into "<nil>", which is exactly the kind of phantom tag that makes a rule
// silently reference a non-existent server.
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// dnsServerHost extracts the host from a DNS server entry's address field:
// both the 1.14 form ({"server": "1.1.1.1"}) and the legacy one
// ({"address": "tls://1.1.1.1:853"}) are understood.
func dnsServerHost(m map[string]any) string {
	v := str(m["server"])
	if v == "" {
		v = str(m["address"])
	}
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	if strings.HasPrefix(v, "[") {
		if j := strings.IndexByte(v, ']'); j > 0 {
			v = v[1:j]
		}
	}
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	return v
}

// isIPLiteral reports whether host is an IP address (no resolution needed).
func isIPLiteral(host string) bool {
	return net.ParseIP(strings.TrimSuffix(host, ".")) != nil
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
