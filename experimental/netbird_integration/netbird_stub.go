//go:build !with_netbird

package netbird_integration

// Stub implementation — netbird integration disabled.
// Compile with -tags "with_netbird" to enable.

type Engine struct{}

type Config struct {
	DeviceName    string
	SetupKey      string
	ManagementURL string
	LogLevel      string
}

type Status struct {
	Running bool
}

func NewEngine(cfg *Config) *Engine {
	return &Engine{}
}

func (e *Engine) Start() error {
	return nil
}

func (e *Engine) Stop() error {
	return nil
}

func (e *Engine) IsRunning() bool {
	return false
}

func (e *Engine) GetStatus() *Status {
	return &Status{Running: false}
}
