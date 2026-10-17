//go:build with_netbird

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sagernet/sing-box/experimental/netbird_integration"
	"github.com/sagernet/sing-box/log"

	"github.com/spf13/cobra"
)

var (
	netbirdConfigPath string
)

var commandNetbirdRun = &cobra.Command{
	Use:   "netbird-run",
	Short: "Run netbird engine (embedded)",
	Long: `Start the netbird engine as an embedded client.

Requires --netbird-config flag or NB_SETUP_KEY/NB_JWT_TOKEN env vars.

Netbird config JSON format:
  {
    "device":  {
      "name":           "my-device",
      "setup_key":      "your-setup-key",
      "management_url": "https://api.netbird.io:443"
    },
    "network": { "mtu": 1280 },
    "log":     { "level": "info" }
  }`,
	Run: func(cmd *cobra.Command, args []string) {
		runNetbird()
	},
}

var commandNetbirdStatus = &cobra.Command{
	Use:   "netbird-status",
	Short: "Show netbird engine status",
	Run: func(cmd *cobra.Command, args []string) {
		// The engine pointer is only assigned by "netbird-run". A standalone
		// "netbird-status" invocation therefore has no engine — the engine is
		// process-local, so it cannot have one — and the old code dereferenced
		// the nil pointer and died with a SIGSEGV instead of reporting that.
		// The first line is kept identical for callers that parse it.
		if nbEngine == nil {
			fmt.Println("netbird: stopped")
			fmt.Println("netbird-status only reports the engine started by this process; none was started here")
			return
		}
		status := nbEngine.GetStatus()
		if status.Running {
			fmt.Println("netbird: running")
		} else {
			fmt.Println("netbird: stopped")
		}
	},
}

var nbEngine *netbird_integration.Engine

func init() {
	commandNetbirdRun.PersistentFlags().StringVarP(&netbirdConfigPath, "netbird-config", "", "", "path to netbird config JSON")
	mainCommand.AddCommand(commandNetbirdRun)
	mainCommand.AddCommand(commandNetbirdStatus)
}

func runNetbird() {
	var cfg *netbird_integration.Config

	if netbirdConfigPath != "" {
		log.Info("reading config from: ", netbirdConfigPath)
		loaded, err := netbird_integration.LoadConfigFile(netbirdConfigPath)
		if err != nil {
			log.Fatal(err)
		}
		// State (WireGuard key, peer state, rule-set files) lives next to the
		// config file — the same rule the run-all path uses for the app's
		// data directory.
		loaded.DataDir = filepath.Dir(netbirdConfigPath)
		cfg = loaded
		log.Info("netbird config loaded: device=", cfg.Device.Name, " mgmt=", cfg.Device.ManagementURL)
	} else {
		// Fallback: build config from env vars
		if os.Getenv("NB_SETUP_KEY") == "" && os.Getenv("NB_JWT_TOKEN") == "" && os.Getenv("NB_PRIVATE_KEY") == "" {
			log.Warn("no config file (--netbird-config) and no NB_SETUP_KEY/NB_JWT_TOKEN/NB_PRIVATE_KEY in the environment")
		}
		cfg = &netbird_integration.Config{
			Device: netbird_integration.DeviceOptions{
				Name:          envOrDefault("NB_DEVICE_NAME", "sing-netbird"),
				ManagementURL: envOrDefault("NB_MANAGEMENT_URL", "https://api.netbird.io:443"),
				SetupKey:      os.Getenv("NB_SETUP_KEY"),
				JWTToken:      os.Getenv("NB_JWT_TOKEN"),
				PrivateKey:    os.Getenv("NB_PRIVATE_KEY"),
			},
			Log: netbird_integration.LogOptions{Level: envOrDefault("NB_LOG_LEVEL", "info")},
		}
	}

	engine := netbird_integration.NewEngine(cfg)

	if err := engine.Start(); err != nil {
		log.Fatal(err)
	}

	nbEngine = engine
	log.Info("netbird engine started successfully")

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("received signal: ", sig)

	if err := engine.Stop(); err != nil {
		log.Error(err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
