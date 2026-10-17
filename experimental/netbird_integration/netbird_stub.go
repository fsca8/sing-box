//go:build !with_netbird

package netbird_integration

import E "github.com/sagernet/sing/common/exceptions"

// Stub implementation — netbird integration disabled.
// Compile with -tags "with_netbird" to enable.

type Engine struct{}

// Config mirrors the real configuration schema. Field shapes must match so
// callers compile unchanged with and without the build tag.
type Config struct {
	Device         DeviceOptions      `json:"device"`
	Network        NetworkOptions     `json:"network"`
	Security       SecurityOptions    `json:"security"`
	LazyConnection *bool              `json:"lazy_connection,omitempty"`
	ExposePorts    []ExposePortConfig `json:"expose_ports,omitempty"`
	Performance    PerformanceOptions `json:"performance"`
	Log            LogOptions         `json:"log"`
	DataDir        string             `json:"-"`
}

// DeviceOptions mirrors the real identity/credentials section.
type DeviceOptions struct {
	Name          string `json:"name,omitempty"`
	ManagementURL string `json:"management_url,omitempty"`
	SetupKey      string `json:"setup_key,omitempty"`
	JWTToken      string `json:"jwt_token,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
}

// NetworkOptions mirrors the real data-path/addressing section.
type NetworkOptions struct {
	KernelTun           bool     `json:"kernel_tun,omitempty"`
	MTU                 int      `json:"mtu,omitempty"`
	WireguardPort       int      `json:"wireguard_port,omitempty"`
	DisableIPv6         bool     `json:"disable_ipv6,omitempty"`
	DisableClientRoutes bool     `json:"disable_client_routes,omitempty"`
	BlockLANAccess      bool     `json:"block_lan_access,omitempty"`
	BlockInbound        bool     `json:"block_inbound,omitempty"`
	NATExternalIPs      []string `json:"nat_external_ips,omitempty"`
	DNSLabels           []string `json:"dns_labels,omitempty"`
}

// SecurityOptions mirrors the real key-exchange section.
type SecurityOptions struct {
	PreSharedKey        string `json:"pre_shared_key,omitempty"`
	Rosenpass           bool   `json:"rosenpass,omitempty"`
	RosenpassPermissive bool   `json:"rosenpass_permissive,omitempty"`
}

// PerformanceOptions mirrors the real tunnel tuning section.
type PerformanceOptions struct {
	PreallocatedBuffersPerPool uint32 `json:"preallocated_buffers_per_pool,omitempty"`
	MaxBatchSize               uint32 `json:"max_batch_size,omitempty"`
}

// LogOptions mirrors the real log section.
type LogOptions struct {
	Level string `json:"level,omitempty"`
}

// ExposePortConfig declares one overlay→local TCP forward.
type ExposePortConfig struct {
	Port   int    `json:"port"`
	Target string `json:"target"`
}

type Status struct {
	Running bool `json:"running"`
}

func NewEngine(cfg *Config) *Engine  { return &Engine{} }
func (e *Engine) Start() error       { return nil }
func (e *Engine) Stop() error        { return nil }
func (e *Engine) IsRunning() bool    { return false }
func (e *Engine) GetStatus() *Status { return &Status{Running: false} }

func ParseConfig(configJSON string) (*Config, error) {
	return nil, E.New("netbird not compiled in (build without with_netbird tag)")
}

func LoadConfigFile(path string) (*Config, error) {
	return nil, E.New("netbird not compiled in (build without with_netbird tag)")
}

// StartAllResult holds the result of StartAll (stub).
type StartAllResult struct {
	ModifiedConfig []byte
	Engine         *Engine
	EngineErr      string
}

// StartAll is a no-op stub when netbird is not compiled in.
func StartAll(cfg *Config, singBoxConfig []byte) (*StartAllResult, error) {
	return &StartAllResult{ModifiedConfig: singBoxConfig}, nil
}
