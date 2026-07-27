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

	// ---- 1. Start netbird engine FIRST ----
	nbCfg := buildNetbirdConfig()
	var nbEngine *netbird_integration.Engine
	if nbCfg.SetupKey != "" || nbCfg.JWTToken != "" {
		nbEngine = netbird_integration.NewEngine(nbCfg)
		if err := nbEngine.Start(); err != nil {
			log.Warn("netbird engine failed to start: ", err)
		} else {
			// Expose the embed client for DNS resolution
			if c := nbEngine.GetClient(); c != nil {
				netbird_integration.SetClient(c)
				log.Info("netbird DNS resolver available")
			}
			log.Info("netbird engine started")
		}
	} else {
		log.Info("no netbird credentials, skipping netbird engine")
	}

	// ---- 2. Read and enhance sing-box config (inject netbird DNS) ----
	// Read raw config, inject netbird entries, write to temp file
	rawConfig, err := os.ReadFile(configPaths[0])
	if err != nil {
		return E.Cause(err, "read config")
	}
	modified, err := injectNetbirdJSON(rawConfig)
	if err != nil {
		return E.Cause(err, "inject netbird DNS")
	}
	tmpPath := configPaths[0] + ".nb-tmp.json"
	if err := os.WriteFile(tmpPath, modified, 0644); err != nil {
		return E.Cause(err, "write temp config")
	}
	defer os.Remove(tmpPath)
	// Override config path to use the modified version
	configPaths = []string{tmpPath}

	optionsList, err := readConfig()
	if err != nil {
		return E.Cause(err, "read config")
	}

	var options option.Options
	if len(optionsList) > 0 {
		options = optionsList[0].options
	}

	// ---- 3. Start sing-box (DNS module will use netbird transport) ----
	_, instanceCancel, err := create(options)
	if err != nil {
		return err
	}
	log.Info("sing-box engine started")

	// ---- 4. Wait for signal ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("received signal: ", sig)

	// ---- 5. Shutdown: netbird first, then sing-box ----
	if nbEngine != nil {
		if err := nbEngine.Stop(); err != nil {
			log.Error("netbird stop error: ", err)
		}
	}
	instanceCancel()
	log.Info("all engines stopped")
	return nil
}

// injectNetbirdJSON adds netbird DNS server and route entries to raw config JSON.
// No hardcoded domains: all DNS queries go through netbird's transport first;
// non-custom domains are forwarded to the fallback DNS by the transport itself.
func injectNetbirdJSON(rawData []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(rawData, &raw); err != nil {
		return nil, err
	}

	// Inject DNS server — netbird as the final resolver
	dnsSection, _ := raw["dns"].(map[string]any)
	if dnsSection == nil {
		dnsSection = make(map[string]any)
		raw["dns"] = dnsSection
	}
	servers, _ := dnsSection["servers"].([]any)
	servers = append(servers, map[string]any{"type": "netbird", "tag": "nb"})
	dnsSection["servers"] = servers
	// Set netbird as the default DNS resolver (no domain rules needed)
	dnsSection["final"] = "nb"

	// Inject route rule for netbird internal IPs
	routeSection, _ := raw["route"].(map[string]any)
	if routeSection == nil {
		routeSection = make(map[string]any)
		raw["route"] = routeSection
	}
	routeRules, _ := routeSection["rules"].([]any)
	routeRules = append(routeRules, map[string]any{
		"ip_cidr":  []string{"100.121.0.0/16"},
		"outbound": "direct",
	})
	routeSection["rules"] = routeRules

	return json.Marshal(raw)
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
