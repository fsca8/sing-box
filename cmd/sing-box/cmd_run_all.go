//go:build with_netbird

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	cleanup, err := runAllEngines(configPaths[0])
	if err != nil {
		return err
	}
	if cleanup == nil {
		return nil // idle, no config
	}
	defer cleanup()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Info("received signal, stopping")
	return nil
}

// runAllEngines starts netbird engine (if enabled), reads/injects config,
// and creates sing-box. Blocks until stopCh receives or sing-box exits.
// Returns a cleanup function (nil on setup failure).
func runAllEngines(cfgPath string) (func(), error) {
	var nbEngine *netbird_integration.Engine
	nbCfg := buildNetbirdConfig()
	hasNBCredentials := nbCfg.SetupKey != "" || nbCfg.JWTToken != ""

	t0 := time.Now()
	var nbDomains []string
	if runAllEnableNetbird && hasNBCredentials {
		nbEngine = netbird_integration.NewEngine(nbCfg)
		if err := nbEngine.Start(); err != nil {
			log.Warn("netbird engine failed to start: ", err)
		} else {
			if c := nbEngine.GetClient(); c != nil {
				netbird_integration.SetClient(c)
				log.Info(fmt.Sprintf("netbird DNS resolver available (t=%.1fs)", time.Since(t0).Seconds()))
				t1 := time.Now()
				domains, err := netbird_integration.WaitAndExtractDomains(c, 5*time.Second)
				log.Info(fmt.Sprintf("netbird sync wait: %.1fs", time.Since(t1).Seconds()))
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

	rawConfig, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Warn("read config: ", err)
		return nil, nil // idle — no config yet
	}
	// Ensure configPaths includes cfgPath for readConfig()
	configPaths = []string{cfgPath}
	if runAllEnableNetbird && nbEngine != nil && nbEngine.IsRunning() {
		t2 := time.Now()
		modified, err := injectNetbirdJSON(rawConfig, nbDomains)
		if err != nil {
			return nil, E.Cause(err, "inject netbird DNS")
		}
		log.Info(fmt.Sprintf("inject config: %.1fs", time.Since(t2).Seconds()))
		tmpPath := cfgPath + ".nb-tmp.json"
		if err := os.WriteFile(tmpPath, modified, 0644); err != nil {
			return nil, E.Cause(err, "write temp config")
		}
		defer os.Remove(tmpPath)
		configPaths = []string{tmpPath}
	}

	optionsList, err := readConfig()
	if err != nil {
		return nil, E.Cause(err, "read config")
	}
	var options option.Options
	if len(optionsList) > 0 {
		options = optionsList[0].options
	}

	t3 := time.Now()
	instance, instanceCancel, err := create(options)
	if err != nil {
		return nil, err
	}
	_ = instance
	log.Info(fmt.Sprintf("sing-box started (t=%.1fs, create:%.1fs)", time.Since(t0).Seconds(), time.Since(t3).Seconds()))

	cleanup := func() {
		if nbEngine != nil {
			if err := nbEngine.Stop(); err != nil {
				log.Error("netbird stop: ", err)
			}
		}
		instanceCancel()
		log.Info("all engines stopped")
	}
	return cleanup, nil
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
	// Don't set final="nb" — only route custom domains through netbird.
	// Other domains keep using the original DNS chain (dns-direct/dns-remote).

	// Inject netbird outbound
	outbounds, _ := raw["outbounds"].([]any)
	outbounds = append(outbounds, map[string]any{"type": "netbird", "tag": "nb-out"})
	// Add a selector outbound so the user can toggle proxy on/off via Clash API
	// without restarting the process. Default: proxy.
	outbounds = append(outbounds, map[string]any{
		"type":      "selector",
		"tag":       "switch",
		"outbounds": []string{"proxy", "direct"},
	})
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
	// Point route final to the selector so the user can toggle proxy on/off
	// via Clash API without restarting. The selector defaults to "proxy".
	routeSection["final"] = "switch"

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
		DataDir:       serviceDataDir(),
	}
	// Try reading from --netbird-config flag, then from DataDir/netbird-config.json
	nbCfgPath := runAllNetbirdConfig
	if nbCfgPath == "" {
		nbCfgPath = filepath.Join(cfg.DataDir, "data", "netbird-config.json")
	}
	if data, err := os.ReadFile(nbCfgPath); err == nil {
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
