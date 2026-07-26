//go:build with_netbird

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/experimental/netbird_integration"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

var (
	runAllNetbirdConfig string
)

var commandRunAll = &cobra.Command{
	Use:   "run-all",
	Short: "Run sing-box and netbird engines simultaneously",
	Long: `Start both sing-box and netbird in one process.

-c: sing-box config (same format as 'run')
--netbird-config: netbird config JSON (optional, falls back to env vars)

Netbird config:
  {"device_name":"...", "setup_key":"...", "management_url":"..."}
`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := runAll(); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	commandRunAll.PersistentFlags().StringVarP(&runAllNetbirdConfig, "netbird-config", "", "", "path to netbird config JSON")
	mainCommand.AddCommand(commandRunAll)
}

func runAll() error {
	if len(configPaths) == 0 && len(configDirectories) == 0 {
		return E.New("-c config path required")
	}

	// ---- 1. Start sing-box via existing create() ----
	optionsList, err := readConfig()
	if err != nil {
		return E.Cause(err, "read config")
	}

	// Use first config only
	var options option.Options
	if len(optionsList) > 0 {
		options = optionsList[0].options
	}

	_, instanceCancel, err := create(options)
	if err != nil {
		return err
	}
	log.Info("sing-box engine started")

	// ---- 2. Start netbird ----
	nbCfg := buildNetbirdConfig()
	var nbEngine *netbird_integration.Engine
	if nbCfg.SetupKey != "" || nbCfg.JWTToken != "" {
		nbEngine = netbird_integration.NewEngine(nbCfg)
		if err := nbEngine.Start(); err != nil {
			log.Warn("netbird engine failed to start: ", err)
		} else {
			log.Info("netbird engine started")
		}
	} else {
		log.Info("no netbird credentials, skipping netbird engine")
	}

	// ---- 3. Wait for signal ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("received signal: ", sig)

	// ---- 4. Shutdown: netbird first, then sing-box ----
	if nbEngine != nil {
		if err := nbEngine.Stop(); err != nil {
			log.Error("netbird stop error: ", err)
		}
	}
	instanceCancel()
	log.Info("all engines stopped")
	return nil
}

func buildNetbirdConfig() *netbird_integration.Config {
	cfg := &netbird_integration.Config{
		DeviceName:    envOrDefault("NB_DEVICE_NAME", "sing-netbird"),
		SetupKey:      os.Getenv("NB_SETUP_KEY"),
		JWTToken:      os.Getenv("NB_JWT_TOKEN"),
		ManagementURL: envOrDefault("NB_MANAGEMENT_URL", "https://api.netbird.io:443"),
		LogLevel:      envOrDefault("NB_LOG_LEVEL", "info"),
	}
	if runAllNetbirdConfig != "" {
		data, err := os.ReadFile(runAllNetbirdConfig)
		if err != nil {
			log.Warn("read --netbird-config: ", err)
			return cfg
		}
		var fileCfg netbird_integration.Config
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			log.Warn("parse --netbird-config: ", err)
			return cfg
		}
		if fileCfg.SetupKey != "" {
			cfg.SetupKey = fileCfg.SetupKey
		}
		if fileCfg.JWTToken != "" {
			cfg.JWTToken = fileCfg.JWTToken
		}
		if fileCfg.ManagementURL != "" {
			cfg.ManagementURL = fileCfg.ManagementURL
		}
		if fileCfg.DeviceName != "" {
			cfg.DeviceName = fileCfg.DeviceName
		}
		if fileCfg.LogLevel != "" {
			cfg.LogLevel = fileCfg.LogLevel
		}
	}
	return cfg
}
