//go:build with_netbird

package netbird_integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	nbembed "github.com/netbirdio/netbird/client/embed"
)

// Engine wraps the netbird embed client.
type Engine struct {
	cfg     *Config
	mu      sync.Mutex
	running bool
	client  *nbembed.Client
}

// Config holds configuration for the netbird engine.
type Config struct {
	DeviceName    string `json:"device_name"`
	SetupKey      string `json:"setup_key"`
	JWTToken      string `json:"jwt_token"`
	ManagementURL string `json:"management_url"`
	AdminURL      string `json:"admin_url"`
	LogLevel      string `json:"log_level"`
}

// Status represents engine status.
type Status struct {
	Running bool `json:"running"`
}

// UnifiedConfig is the top-level config that wraps both engines.
type UnifiedConfig struct {
	Netbird *Config `json:"netbird"`
	// SingBox is left unstructured — sing-box parses its own section.
	SingBox json.RawMessage `json:"sing_box"`
}

// NewEngine creates a new netbird engine wrapper with the given config.
func NewEngine(cfg *Config) *Engine {
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = "sing-netbird"
	}
	if cfg.ManagementURL == "" {
		cfg.ManagementURL = "https://api.netbird.io:443"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return &Engine{cfg: cfg}
}

// Start starts the netbird engine using the config from NewEngine.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		return fmt.Errorf("netbird engine already running")
	}

	// Fall back to env vars for credentials if not in config
	setupKey := e.cfg.SetupKey
	jwtToken := e.cfg.JWTToken
	if setupKey == "" {
		setupKey = os.Getenv("NB_SETUP_KEY")
	}
	if jwtToken == "" {
		jwtToken = os.Getenv("NB_JWT_TOKEN")
	}
	if setupKey == "" && jwtToken == "" {
		log.Warn("netbird: no SetupKey or JWTToken set, engine may not authenticate")
	}

	opts := nbembed.Options{
		DeviceName:    e.cfg.DeviceName,
		SetupKey:      setupKey,
		JWTToken:      jwtToken,
		ManagementURL: e.cfg.ManagementURL,
		LogLevel:      e.cfg.LogLevel,
	}

	client, err := nbembed.New(opts)
	if err != nil {
		return fmt.Errorf("netbird embed new: %w", err)
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startCancel()

	if err := client.Start(startCtx); err != nil {
		return fmt.Errorf("netbird embed start: %w", err)
	}

	e.client = client
	e.running = true
	log.Info("netbird: engine started")
	return nil
}

// Stop stops the netbird engine.
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running || e.client == nil {
		return nil
	}

	log.Info("netbird: stopping engine")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.client.Stop(ctx); err != nil {
		return fmt.Errorf("netbird embed stop: %w", err)
	}

	e.running = false
	e.client = nil
	log.Info("netbird: engine stopped")
	return nil
}

// IsRunning returns whether the engine is running.
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// GetClient returns the underlying netbird embed client, if available.
func (e *Engine) GetClient() *nbembed.Client {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.client
}

// GetStatus returns the current engine status.
func (e *Engine) GetStatus() *Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return &Status{Running: e.running}
}

// ReadUnifiedConfig reads a unified JSON config file and returns the parsed structure.
func ReadUnifiedConfig(path string) (*UnifiedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg UnifiedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}
