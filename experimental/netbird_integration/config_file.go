//go:build with_netbird

package netbird_integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

// The netbird engine is configured by a single JSON file (netbird-config.json),
// the counterpart of the sing-box config file: one document, grouped into
// sections, with the same decoding rules — an unknown key is an error instead
// of a silently ignored option. Every key documented here is actually read by
// the engine; a key without a consumer is a bug, not a convenience.
//
// Top level:
//
//	{
//	  "device":     { ... },   // identity + credentials
//	  "network":    { ... },   // tunnel/data-path behaviour
//	  "security":   { ... },   // pre-shared key, Rosenpass
//	  "lazy_connection": true, // tri-state, see the field
//	  "expose_ports": [ ... ], // overlay → local TCP forwards
//	  "performance": { ... },  // tunnel buffer/batch tuning
//	  "log":        { ... }    // engine log level
//	}
//
// The data directory (state files, rule-set files, logs) is NOT part of this
// file: the host derives it from the location of the sing-box config file, so
// there is exactly one rule for where state lives.

// Config is the netbird engine configuration.
type Config struct {
	Device DeviceOptions `json:"device"`
	// Network holds data-path and addressing behaviour.
	Network NetworkOptions `json:"network"`
	// Security holds tunnel authentication / key-exchange options.
	Security SecurityOptions `json:"security"`
	// LazyConnection is a tri-state local override for netbird's lazy
	// connections. nil (absent) defers to the management feature flag; true
	// and false override it in both directions. Set false when the first
	// request to a peer must not wait for an on-demand connection.
	LazyConnection *bool `json:"lazy_connection,omitempty"`
	// ExposePorts lists overlay→local TCP forwards. Each entry makes the
	// engine listen on the given port inside the overlay (netstack) and
	// forward accepted connections to the local target — the userspace
	// netstack cannot deliver inbound connections to processes listening on
	// the host kernel stack.
	ExposePorts []ExposePortConfig `json:"expose_ports,omitempty"`
	// Performance tunes the tunnel's buffer pool and batching.
	Performance PerformanceOptions `json:"performance"`
	// Log configures the engine log.
	Log LogOptions `json:"log"`
	// DataDir is engine-internal and never read from the file: the host (CLI
	// flag, embed host) points the engine at its data directory. Excluded
	// from JSON so a hand-written "data_dir" key cannot silently redirect
	// state to another location (the legacy parser warns and ignores it).
	DataDir string `json:"-"`
}

// DeviceOptions identifies this peer and carries its credentials.
type DeviceOptions struct {
	// Name is this peer's name in the network.
	// Default: "sing-netbird".
	Name string `json:"name,omitempty"`
	// ManagementURL overrides the default management server URL.
	// Default: "https://api.netbird.io:443".
	ManagementURL string `json:"management_url,omitempty"`
	// SetupKey authenticates with a reusable/single-use setup key.
	SetupKey string `json:"setup_key,omitempty"`
	// JWTToken authenticates with a JWT issued by the management server.
	JWTToken string `json:"jwt_token,omitempty"`
	// PrivateKey authenticates as an existing device with its WireGuard
	// private key, e.g. to reuse another client's identity.
	//
	// SetupKey, JWTToken and PrivateKey are alternatives: exactly one of
	// them is normally set. Environment fallbacks (NB_SETUP_KEY,
	// NB_JWT_TOKEN, NB_PRIVATE_KEY) are used for the credentials left empty.
	PrivateKey string `json:"private_key,omitempty"`
}

// NetworkOptions configures the data path, addressing and inbound policy.
type NetworkOptions struct {
	// KernelTun enables the Linux kernel-TUN data path: netbird creates a
	// real kernel TUN (wt0) and a single static route sends the overlay
	// CIDR straight to it, bypassing the sing-box TUN stack (single TCP
	// termination, official-client performance). Only honored on Linux and
	// requires root; Windows/Android always use the userspace netstack.
	KernelTun bool `json:"kernel_tun,omitempty"`
	// MTU is the tunnel interface MTU. Valid range: 576..8192, 0 (absent)
	// keeps the stored/default value (1280). Raise it (e.g. 1400) when the
	// overlay must carry protocols that need larger datagrams.
	MTU int `json:"mtu,omitempty"`
	// WireguardPort is the local WireGuard listen port. 0 (absent) picks a
	// random port.
	WireguardPort int `json:"wireguard_port,omitempty"`
	// DisableIPv6 disables IPv6 overlay addressing.
	DisableIPv6 bool `json:"disable_ipv6,omitempty"`
	// DisableClientRoutes stops the engine from installing the routes it
	// learns from the management server (the host routing table stays
	// untouched; overlay traffic is steered by the sing-box rules instead).
	DisableClientRoutes bool `json:"disable_client_routes,omitempty"`
	// BlockLANAccess prevents this peer from reaching the host's local
	// network (RFC 1918, link-local, loopback) while acting as a routing
	// peer, so it cannot become a stepping stone into the LAN.
	BlockLANAccess bool `json:"block_lan_access,omitempty"`
	// BlockInbound rejects all inbound connections from peers.
	BlockInbound bool `json:"block_inbound,omitempty"`
	// NATExternalIPs maps external IPs to internal IPs or interfaces for
	// ICE candidate selection (same format as the agent's --external-ip-map
	// flag, e.g. "203.0.113.7/32=10.0.0.2/32" or "203.0.113.7/32=eth0").
	NATExternalIPs []string `json:"nat_external_ips,omitempty"`
	// DNSLabels adds DNS labels owned by this peer.
	DNSLabels []string `json:"dns_labels,omitempty"`
}

// SecurityOptions configures tunnel authentication and key exchange.
type SecurityOptions struct {
	// PreSharedKey is the WireGuard pre-shared key of the tunnel interface.
	PreSharedKey string `json:"pre_shared_key,omitempty"`
	// Rosenpass enables the Rosenpass post-quantum key exchange.
	Rosenpass bool `json:"rosenpass,omitempty"`
	// RosenpassPermissive keeps a Rosenpass-enabled peer able to connect to
	// peers that do not run Rosenpass (falling back to the plain PSK).
	RosenpassPermissive bool `json:"rosenpass_permissive,omitempty"`
}

// PerformanceOptions tunes the tunnel's memory/throughput behaviour. A zero
// value means "leave the default": the fields are passed only when set.
//
// NOTE: these values are process-global in netbird's embed API — a value set
// here also becomes the default for every client constructed afterwards in
// the same process.
type PerformanceOptions struct {
	// PreallocatedBuffersPerPool caps the per-tunnel buffer pool. Zero keeps
	// the pool unbounded; a lower value trades throughput for a tighter
	// memory ceiling.
	PreallocatedBuffersPerPool uint32 `json:"preallocated_buffers_per_pool,omitempty"`
	// MaxBatchSize overrides how many packets the tunnel reads/writes per
	// syscall, which also bounds eager buffer allocation per worker. Zero
	// uses the platform default.
	MaxBatchSize uint32 `json:"max_batch_size,omitempty"`
}

// LogOptions configures the engine log. The destination is not configurable:
// the engine always writes to "<data dir>/logs/netbird.log" (the same log
// directory as the sing-box engine) so the app's log viewer can list it.
type LogOptions struct {
	// Level is the netbird log level: trace, debug, info, warn, error.
	// Default: "info".
	Level string `json:"level,omitempty"`
}

// ExposePortConfig declares one overlay→local TCP forward.
type ExposePortConfig struct {
	// Port is the port to listen on inside the netbird overlay.
	Port int `json:"port"`
	// Target is the local host:port to forward to (e.g. "127.0.0.1:8022").
	Target string `json:"target"`
}

// defaultDeviceName is this peer's default name in the network.
const defaultDeviceName = "sing-netbird"

// defaultManagementURL is netbird's hosted management server.
const defaultManagementURL = "https://api.netbird.io:443"

// defaultLogLevel is the engine log level used when none is configured.
const defaultLogLevel = "info"

// minTunnelMTU and maxTunnelMTU bound the tunnel MTU (netbird's own range).
const (
	minTunnelMTU = 576
	maxTunnelMTU = 8192
)

// dataDir returns the engine data directory. Embed hosts always set one; CLI
// runs without a config location fall back to the OS temp directory so a stray
// run cannot litter the working directory.
func (c *Config) dataDir() string {
	if c != nil && c.DataDir != "" {
		return c.DataDir
	}
	return os.TempDir()
}

// stateDir is netbird's own state directory (<data dir>/nb-state): the
// persisted WireGuard key and peer state, plus the rule-set files the injected
// sing-box config references. Everything in here is engine-internal — it is
// not a place to edit by hand, and the files inside are regenerated.
func (c *Config) stateDir() string {
	return filepath.Join(c.dataDir(), "nb-state")
}

// logsDir is the engine log directory (<data dir>/logs), shared with the
// sing-box engine log.
func (c *Config) logsDir() string {
	return filepath.Join(c.dataDir(), "logs")
}

// envFallback returns value, or the named environment variable when value is
// empty (the documented credential fallback for CLI runs).
func envFallback(value, envKey string) string {
	if value != "" {
		return value
	}
	return os.Getenv(envKey)
}

// optionalUint32 turns a "0 means unset" knob into the pointer form netbird's
// embed API uses, so an unset field keeps netbird's own default instead of
// being pinned to zero.
func optionalUint32(value uint32) *uint32 {
	if value == 0 {
		return nil
	}
	return &value
}

// applyDefaults fills the values every caller relies on, so no consumer has to
// repeat the fallbacks.
func (c *Config) applyDefaults() {
	if c.Device.Name == "" {
		c.Device.Name = defaultDeviceName
	}
	if c.Device.ManagementURL == "" {
		c.Device.ManagementURL = defaultManagementURL
	}
	if c.Log.Level == "" {
		c.Log.Level = defaultLogLevel
	}
}

// hasCredentials reports whether any authentication material is available,
// either from the config file or from the environment fallbacks. PrivateKey
// counts: a config that only holds a private key authenticates as an existing
// device and must not be treated as "no credentials".
func (c *Config) hasCredentials() bool {
	if c == nil {
		return false
	}
	return c.Device.SetupKey != "" || c.Device.JWTToken != "" || c.Device.PrivateKey != "" ||
		os.Getenv("NB_SETUP_KEY") != "" || os.Getenv("NB_JWT_TOKEN") != "" || os.Getenv("NB_PRIVATE_KEY") != ""
}

// Validate rejects a structurally invalid configuration. Invalid values are
// hard errors (the engine refuses to start with a clear message) rather than
// warnings: a silently ignored option is exactly the failure mode this
// configuration file exists to avoid.
func (c *Config) Validate() error {
	if c.Network.MTU != 0 && (c.Network.MTU < minTunnelMTU || c.Network.MTU > maxTunnelMTU) {
		return fmt.Errorf("network.mtu: %d out of range (%d..%d, 0 keeps the default)", c.Network.MTU, minTunnelMTU, maxTunnelMTU)
	}
	if c.Network.WireguardPort != 0 && (c.Network.WireguardPort < 1 || c.Network.WireguardPort > 65535) {
		return fmt.Errorf("network.wireguard_port: %d out of range (1..65535, 0 picks a random port)", c.Network.WireguardPort)
	}
	if c.Device.ManagementURL != "" {
		u, err := url.Parse(c.Device.ManagementURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("device.management_url: %q is not an http(s) URL with a host", c.Device.ManagementURL)
		}
	}
	seenPorts := make(map[int]struct{}, len(c.ExposePorts))
	for i, ep := range c.ExposePorts {
		if ep.Port < 1 || ep.Port > 65535 {
			return fmt.Errorf("expose_ports[%d].port: %d out of range (1..65535)", i, ep.Port)
		}
		if _, duplicate := seenPorts[ep.Port]; duplicate {
			return fmt.Errorf("expose_ports[%d].port: %d is declared twice", i, ep.Port)
		}
		seenPorts[ep.Port] = struct{}{}
		if ep.Target == "" || !strings.Contains(ep.Target, ":") {
			return fmt.Errorf("expose_ports[%d].target: %q must be a host:port address", i, ep.Target)
		}
	}
	// More than one credential is not fatal (the engine picks them up in a
	// fixed order), but it is almost always a leftover from a half-edited
	// file, so say so instead of silently ignoring one of them.
	credentialCount := 0
	for _, value := range []string{c.Device.SetupKey, c.Device.JWTToken, c.Device.PrivateKey} {
		if value != "" {
			credentialCount++
		}
	}
	if credentialCount > 1 {
		log.Warn("netbird: more than one credential is configured (setup_key/jwt_token/private_key); setup_key wins over jwt_token over private_key")
	}
	if c.Network.KernelTun {
		log.Info("netbird: network.kernel_tun is only honored on Linux; other platforms use the userspace netstack")
	}
	return nil
}

// ParseConfig parses a netbird config JSON string (used by the embed hosts,
// which receive the document from their own storage rather than a file path).
func ParseConfig(configJSON string) (*Config, error) {
	return parseConfigBytes([]byte(configJSON), "netbird config")
}

// LoadConfigFile reads and parses a netbird config file. Any error (unreadable
// file, malformed JSON, unknown key, invalid value) is returned to the caller:
// a config the user can see but the engine ignores is worse than a startup
// error naming the offending key.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read netbird config %s: %w", path, err)
	}
	return parseConfigBytes(data, path)
}

// parseConfigBytes decodes a config document. Documents in the legacy flat
// format (device_name/setup_key/... at the top level, as written by earlier
// versions of the app) are still accepted: their keys are lifted into the
// grouped sections and a deprecation warning names them, so an upgrade can
// never leave a working installation unable to start.
func parseConfigBytes(data []byte, source string) (*Config, error) {
	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawKeys); err != nil {
		return nil, fmt.Errorf("parse netbird config %s: %w", source, err)
	}
	legacy := make(map[string]json.RawMessage)
	for _, key := range legacyFlatKeys {
		if value, loaded := rawKeys[key]; loaded {
			legacy[key] = value
			delete(rawKeys, key)
		}
	}
	grouped, err := json.Marshal(rawKeys)
	if err != nil {
		return nil, fmt.Errorf("parse netbird config %s: %w", source, err)
	}
	cfg := &Config{}
	if err := strictUnmarshal(grouped, cfg); err != nil {
		return nil, fmt.Errorf("parse netbird config %s: %w", source, err)
	}
	if len(legacy) > 0 {
		if err := applyLegacyConfig(legacy, cfg); err != nil {
			return nil, fmt.Errorf("parse netbird config %s (legacy flat format): %w", source, err)
		}
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("netbird config %s: %w", source, err)
	}
	return cfg, nil
}

// strictUnmarshal decodes JSON into v and rejects unknown keys, mirroring the
// strictness of sing-box's own config decoder: a typo must not turn into a
// silently missing option.
func strictUnmarshal(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

// legacyFlatKeys are the top-level keys of the pre-refactor flat config file.
// "admin_url" and "package_name" were never read by the engine at all; they
// are listed here only so an old file still starts (with a warning) instead of
// failing on an unknown key.
//
// "expose_ports" is deliberately absent: the key is part of the current schema
// with the same shape, so it is handled by the grouped decoder (and must not
// trigger a deprecation warning for a file that only uses the new format).
var legacyFlatKeys = []string{
	"device_name",
	"setup_key",
	"jwt_token",
	"management_url",
	"admin_url",
	"log_level",
	"data_dir",
	"kernel_tun",
	"private_key",
	"package_name",
}

// legacyConfig is the pre-refactor flat config file schema.
type legacyConfig struct {
	DeviceName    string `json:"device_name"`
	SetupKey      string `json:"setup_key"`
	JWTToken      string `json:"jwt_token"`
	ManagementURL string `json:"management_url"`
	AdminURL      string `json:"admin_url"`
	LogLevel      string `json:"log_level"`
	DataDir       string `json:"data_dir"`
	KernelTun     bool   `json:"kernel_tun"`
	PrivateKey    string `json:"private_key"`
	PackageName   string `json:"package_name"`
}

// applyLegacyConfig lifts the legacy flat keys into cfg. Values already set by
// a grouped section win, so a half-migrated file behaves predictably.
func applyLegacyConfig(legacy map[string]json.RawMessage, cfg *Config) error {
	subset, err := json.Marshal(legacy)
	if err != nil {
		return err
	}
	var old legacyConfig
	if err := strictUnmarshal(subset, &old); err != nil {
		return err
	}
	names := make([]string, 0, len(legacy))
	for key := range legacy {
		names = append(names, key)
	}
	sort.Strings(names)
	log.Warn("netbird: config uses the deprecated flat format (", strings.Join(names, ", "), "); migrate to the grouped format (device/network/security/log)")
	if old.AdminURL != "" {
		log.Warn("netbird: admin_url is ignored; it was never read by the engine")
	}
	if old.PackageName != "" {
		log.Warn("netbird: package_name is ignored; it was never read by the engine")
	}
	if old.DataDir != "" {
		log.Warn("netbird: data_dir is ignored; the data directory is derived from the sing-box config location")
	}
	if cfg.Device.Name == "" {
		cfg.Device.Name = old.DeviceName
	}
	if cfg.Device.SetupKey == "" {
		cfg.Device.SetupKey = old.SetupKey
	}
	if cfg.Device.JWTToken == "" {
		cfg.Device.JWTToken = old.JWTToken
	}
	if cfg.Device.PrivateKey == "" {
		cfg.Device.PrivateKey = old.PrivateKey
	}
	if cfg.Device.ManagementURL == "" {
		cfg.Device.ManagementURL = old.ManagementURL
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = old.LogLevel
	}
	if !cfg.Network.KernelTun {
		cfg.Network.KernelTun = old.KernelTun
	}
	return nil
}
