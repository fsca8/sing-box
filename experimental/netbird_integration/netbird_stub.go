//go:build !with_netbird

package netbird_integration

import (
	"encoding/json"

	E "github.com/sagernet/sing/common/exceptions"
)

// Stub implementation — netbird integration disabled.
// Compile with -tags "with_netbird" to enable.

type Engine struct{}

type Config struct {
	DeviceName    string             `json:"device_name"`
	SetupKey      string             `json:"setup_key"`
	JWTToken      string             `json:"jwt_token"`
	ManagementURL string             `json:"management_url"`
	AdminURL      string             `json:"admin_url"`
	LogLevel      string             `json:"log_level"`
	DataDir       string             `json:"data_dir"`
	KernelTun     bool               `json:"kernel_tun"`
	PrivateKey    string             `json:"private_key"`
	ExposePorts   []ExposePortConfig `json:"expose_ports"`
}

// ExposePortConfig declares one overlay→local TCP forward.
type ExposePortConfig struct {
	Port   int    `json:"port"`
	Target string `json:"target"`
}

type Status struct {
	Running bool `json:"running"`
}

// UnifiedConfig stub mirrors the real struct (field shapes must match so that
// callers compile unchanged with and without the build tag).
type UnifiedConfig struct {
	Netbird *Config         `json:"netbird"`
	SingBox json.RawMessage `json:"sing_box"`
}

func NewEngine(cfg *Config) *Engine  { return &Engine{} }
func (e *Engine) Start() error       { return nil }
func (e *Engine) Stop() error        { return nil }
func (e *Engine) IsRunning() bool    { return false }
func (e *Engine) GetStatus() *Status { return &Status{Running: false} }
func ReadUnifiedConfig(path string) (*UnifiedConfig, error) {
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
