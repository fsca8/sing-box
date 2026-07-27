//go:build with_netbird

package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/experimental/netbird_integration"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

var (
	runAllNetbirdConfig string
	runAllEnableNetbird bool
)

var commandRunAll = &cobra.Command{
	Use:   "run-all",
	Short: "Run sing-box and netbird engines simultaneously",
	Long: `Start sing-box and optionally netbird in one process.

-c: sing-box config (same format as 'run')
--netbird-config: netbird config JSON (optional, falls back to env vars)
--enable-netbird: start netbird engine alongside sing-box (default: false)

When --enable-netbird is true and netbird config is available:
  - netbird engine starts first
  - DNS queries route through netbird's handler chain
  - Traffic to 100.121.0.0/16 goes through netbird tunnel
When --enable-netbird is false (default), runs sing-box only.

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
	commandRunAll.PersistentFlags().BoolVarP(&runAllEnableNetbird, "enable-netbird", "", false, "start netbird engine alongside sing-box")
	mainCommand.AddCommand(commandRunAll)
}

func runAll() error {
	if len(configPaths) == 0 && len(configDirectories) == 0 {
		return E.New("-c config path required")
	}

	var nbEngine *netbird_integration.Engine
	nbCfg := buildNetbirdConfig()
	hasNBCredentials := nbCfg.SetupKey != "" || nbCfg.JWTToken != ""

	// ---- 1. Start netbird engine FIRST (if enabled) ----
	var nbDomains []string
	if runAllEnableNetbird && hasNBCredentials {
		nbEngine = netbird_integration.NewEngine(nbCfg)
		if err := nbEngine.Start(); err != nil {
			log.Warn("netbird engine failed to start: ", err)
		} else {
			if c := nbEngine.GetClient(); c != nil {
				netbird_integration.SetClient(c)
				log.Info("netbird DNS resolver available")
				// Wait for initial sync to get custom domains
				domains, err := netbird_integration.WaitAndExtractDomains(c, 30*time.Second)
				if err != nil {
					log.Warn("wait for netbird sync: ", err)
				} else {
					nbDomains = domains
					log.Info(fmt.Sprintf("netbird custom domains: %v", nbDomains))
				}
			}
			log.Info("netbird engine started")
		}
	} else if runAllEnableNetbird && !hasNBCredentials {
		log.Warn("--enable-netbird is set but no netbird credentials found, skipping")
	} else {
		log.Info("netbird disabled, running sing-box only")
	}

	// ---- 2. Read and enhance sing-box config ----
	rawConfig, err := os.ReadFile(configPaths[0])
	if err != nil {
		return E.Cause(err, "read config")
	}
	// Inject netbird DNS/route config only when netbird is active
	if runAllEnableNetbird && nbEngine != nil && nbEngine.IsRunning() {
		modified, err := injectNetbirdJSON(rawConfig, nbDomains)
		if err != nil {
			return E.Cause(err, "inject netbird DNS")
		}
		tmpPath := configPaths[0] + ".nb-tmp.json"
		if err := os.WriteFile(tmpPath, modified, 0644); err != nil {
			return E.Cause(err, "write temp config")
		}
		defer os.Remove(tmpPath)
		configPaths = []string{tmpPath}
	}

	optionsList, err := readConfig()
	if err != nil {
		return E.Cause(err, "read config")
	}

	var options option.Options
	if len(optionsList) > 0 {
		options = optionsList[0].options
	}

	// ---- 3. Start sing-box ----
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
func injectNetbirdJSON(rawData []byte, customDomains []string) ([]byte, error) {
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
	dnsSection["final"] = "nb"

	// Inject netbird outbound
	outbounds, _ := raw["outbounds"].([]any)
	outbounds = append(outbounds, map[string]any{"type": "netbird", "tag": "nb-out"})
	raw["outbounds"] = outbounds

	// Inject route rule for netbird internal IPs — use netbird outbound instead of direct
	routeSection, _ := raw["route"].(map[string]any)
	if routeSection == nil {
		routeSection = make(map[string]any)
		raw["route"] = routeSection
	}
	routeRules, _ := routeSection["rules"].([]any)
	// Remove any existing rules for 100.121.0.0/16 (from earlier config edits)
	var cleaned []any
	for _, r := range routeRules {
		rule, ok := r.(map[string]any)
		skip := false
		if ok {
			if cidrs, ok := rule["ip_cidr"].([]any); ok {
				for _, c := range cidrs {
					if fmt.Sprint(c) == "100.121.0.0/16" {
						skip = true
						break
					}
				}
			}
		}
		if !skip {
			cleaned = append(cleaned, r)
		}
	}
	// Add the netbird outbound rule at the TOP of route rules,
	// so ip_cidr matching happens before domain/rule_set matching.
	var nbRouteRules []any
	nbRouteRules = append(nbRouteRules, map[string]any{
		"ip_cidr":  []string{"100.121.0.0/16"},
		"outbound": "nb-out",
	})
	// Add domain-specific route rules for each custom domain — before
	// geosite rules so they take priority over geosite-geolocation-!cn.
	for _, d := range customDomains {
		// Strip trailing dot if present (protobuf domains end with '.')
		clean := strings.TrimSuffix(d, ".")
		nbRouteRules = append(nbRouteRules, map[string]any{
			"domain_suffix": clean,
			"outbound":      "nb-out",
		})
	}
	routeSection["rules"] = append(nbRouteRules, cleaned...)

	// Add domain-specific DNS rules for each custom domain
	for _, d := range customDomains {
		clean := strings.TrimSuffix(d, ".")
		rules, _ := dnsSection["rules"].([]any)
		rules = append(rules, map[string]any{
			"domain_suffix": clean,
			"server":        "nb",
		})
		dnsSection["rules"] = rules
	}

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
