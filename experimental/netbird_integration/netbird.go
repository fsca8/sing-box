//go:build with_netbird

package netbird_integration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	nbembed "github.com/netbirdio/netbird/client/embed"
)

// Engine wraps the netbird embed client.
type Engine struct {
	mu      sync.Mutex
	running bool
	client  *nbembed.Client
}

// Config holds configuration for the netbird engine.
type Config struct {
	DeviceName    string
	SetupKey      string
	ManagementURL string
	LogLevel      string
}

// Status represents engine status.
type Status struct {
	Running bool
}

// NewEngine creates a new netbird engine wrapper.
func NewEngine(cfg *Config) *Engine {
	return &Engine{}
}

// Start starts the netbird engine.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		return fmt.Errorf("netbird engine already running")
	}

	opts := nbembed.Options{
		DeviceName:    envOrDefault("NB_DEVICE_NAME", "sing-netbird"),
		SetupKey:      envOrDefault("NB_SETUP_KEY", ""),
		ManagementURL: envOrDefault("NB_MANAGEMENT_URL", "https://api.netbird.io:443"),
		LogLevel:      envOrDefault("NB_LOG_LEVEL", "info"),
	}

	client, err := nbembed.New(opts)
	if err != nil {
		return fmt.Errorf("netbird embed new: %w", err)
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
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

// GetStatus returns the current engine status.
func (e *Engine) GetStatus() *Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return &Status{Running: e.running}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
