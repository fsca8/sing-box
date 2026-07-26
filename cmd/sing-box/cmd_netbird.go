//go:build with_netbird

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/experimental/netbird_integration"

	"github.com/spf13/cobra"
)

var commandNetbirdRun = &cobra.Command{
	Use:   "netbird-run",
	Short: "Run netbird engine (embedded, requires NB_SETUP_KEY or NB_JWT_TOKEN)",
	Run: func(cmd *cobra.Command, args []string) {
		runNetbird()
	},
}

var commandNetbirdStatus = &cobra.Command{
	Use:   "netbird-status",
	Short: "Show netbird engine status",
	Run: func(cmd *cobra.Command, args []string) {
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
	mainCommand.AddCommand(commandNetbirdRun)
	mainCommand.AddCommand(commandNetbirdStatus)
}

func runNetbird() {
	if os.Getenv("NB_SETUP_KEY") == "" && os.Getenv("NB_JWT_TOKEN") == "" {
		log.Warn("NB_SETUP_KEY or NB_JWT_TOKEN not set, netbird may not authenticate")
	}

	cfg := &netbird_integration.Config{
		DeviceName:    envOrDefault("NB_DEVICE_NAME", "sing-netbird"),
		SetupKey:      os.Getenv("NB_SETUP_KEY"),
		ManagementURL: envOrDefault("NB_MANAGEMENT_URL", "https://api.netbird.io:443"),
		LogLevel:      envOrDefault("NB_LOG_LEVEL", "info"),
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
