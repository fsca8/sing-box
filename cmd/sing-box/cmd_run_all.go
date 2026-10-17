//go:build with_netbird

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/experimental/monitor"
	"github.com/sagernet/sing-box/experimental/netbird_integration"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
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
  - Traffic to the netbird overlay CIDR goes through the netbird tunnel
When --enable-netbird is false (default), runs sing-box only.

Netbird config (--netbird-config, defaults to <config dir>/netbird-config.json):
  {
    "device":  {"name": "my-device", "management_url": "...", "setup_key": "..."},
    "network": {"mtu": 1280, "kernel_tun": false},
    "expose_ports": [{"port": 8022, "target": "127.0.0.1:8022"}]
  }
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
	if len(configPaths) == 0 {
		if len(configDirectories) > 0 {
			return E.New("run-all needs a single config file: -c <path> (config directories are not supported)")
		}
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

	// /monitor/shutdown 优雅关闭支持:
	// Windows 上 Flutter 的 Process.kill 等价 TerminateProcess, 引擎拿不到
	// 清理机会 (TUN 适配器/路由残留)。Flutter stop() 先 POST /monitor/shutdown,
	// 这里关闭 stopCh 让 runAll 走与信号退出完全相同的 cleanup() 路径。
	stopCh := make(chan struct{})
	monitor.SetShutdownHandler(func() error {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
		return nil
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
		log.Info("received signal, stopping")
	case <-stopCh:
		log.Info("received shutdown request, stopping")
	}
	return nil
}

// runAllEngines starts netbird engine (if enabled), reads/injects config,
// and creates sing-box. Blocks until stopCh receives or sing-box exits.
// Returns a cleanup function (nil on setup failure).
func runAllEngines(cfgPath string) (func(), error) {
	t0 := time.Now()
	var nbEngine *netbird_integration.Engine
	var startAllResult *netbird_integration.StartAllResult

	if runAllEnableNetbird {
		nbCfg := buildNetbirdConfig(cfgPath)
		rawConfig, err := os.ReadFile(cfgPath)
		if err != nil {
			log.Warn("read config: ", err)
			return nil, nil
		}
		startAllResult, err = netbird_integration.StartAll(nbCfg, rawConfig)
		if err != nil {
			log.Warn("netbird start: ", err)
		}
		if startAllResult != nil {
			nbEngine = startAllResult.Engine
			if startAllResult.ModifiedConfig != nil {
				tmpPath := cfgPath + ".nb-tmp.json"
				// 0600：注入后的配置含 uuid/密码等凭据（原文件可能也是 0600）
				if err := os.WriteFile(tmpPath, startAllResult.ModifiedConfig, 0o600); err != nil {
					return nil, E.Cause(err, "write temp config")
				}
				defer os.Remove(tmpPath)
				configPaths = []string{tmpPath}
				log.Info(fmt.Sprintf("netbird: injected config written (t=%.1fs)", time.Since(t0).Seconds()))
			}
		}
	} else {
		log.Info("netbird disabled, running sing-box only")
	}

	// Ensure configPaths includes cfgPath for readConfig()
	if len(configPaths) == 0 {
		configPaths = []string{cfgPath}
	}

	optionsList, err := readConfig()
	if err != nil {
		return nil, E.Cause(err, "read config")
	}
	var options option.Options
	if len(optionsList) > 0 {
		options = optionsList[0].options
	}

	t1 := time.Now()
	instance, instanceCancel, err := create(options)
	if err != nil {
		return nil, err
	}
	_ = instance
	log.Info(fmt.Sprintf("sing-box started (t=%.1fs)", time.Since(t1).Seconds()))

	// Kernel-TUN mode: install the single static overlay → wt0 route so
	// netbird traffic bypasses the sing-box TUN stack. Our rule priority
	// (2000) always precedes sing-tun's (9000), so ordering vs the TUN
	// setup above does not matter. Non-fatal on failure: sing-box still
	// runs and overlay traffic falls back to the TUN path (degraded).
	var kernelRouteCleanup func()
	if nbEngine != nil && startAllResult != nil && netbird_integration.IsKernelMode() {
		kernelRouteCleanup, err = netbird_integration.SetupKernelRoute(startAllResult.NetworkCIDR)
		if err != nil {
			log.Warn("netbird kernel route: ", err)
		} else {
			log.Info(fmt.Sprintf("netbird kernel route installed (overlay → wt0, t=%.1fs)", time.Since(t0).Seconds()))
		}
	}

	cleanup := func() {
		if kernelRouteCleanup != nil {
			kernelRouteCleanup()
			log.Info("netbird kernel route removed")
		}
		if nbEngine != nil {
			// Shutdown（而非 Stop）：同时关掉网络变化 watcher，否则它可能
			// 在拆除后把刚停掉的引擎重新拉起来。
			nbEngine.Shutdown()
		}
		instanceCancel()
		log.Info("all engines stopped")
	}
	return cleanup, nil
}

// buildNetbirdConfig loads the netbird engine config for this process.
//
// The config file is the documented configuration surface and wins over the
// environment; the NB_* variables are the fallback for a run without a file
// (and credentials may always be supplied by NB_SETUP_KEY / NB_JWT_TOKEN /
// NB_PRIVATE_KEY, see DeviceOptions). The data directory is never read from
// the file: it is the directory of the sing-box config, so both engines always
// agree on where state lives.
//
// Returns nil when a config file exists but cannot be used: netbird is then
// disabled with a loud error instead of starting with silently dropped
// options — that failure mode is exactly what this format exists to prevent.
func buildNetbirdConfig(cfgPath string) *netbird_integration.Config {
	dataDir := filepath.Dir(cfgPath)
	envCfg := &netbird_integration.Config{
		Device: netbird_integration.DeviceOptions{
			Name:          envOrDefault("NB_DEVICE_NAME", "sing-netbird"),
			ManagementURL: envOrDefault("NB_MANAGEMENT_URL", "https://api.netbird.io:443"),
			SetupKey:      os.Getenv("NB_SETUP_KEY"),
			JWTToken:      os.Getenv("NB_JWT_TOKEN"),
			PrivateKey:    os.Getenv("NB_PRIVATE_KEY"),
		},
		Log:     netbird_integration.LogOptions{Level: envOrDefault("NB_LOG_LEVEL", "info")},
		DataDir: dataDir,
	}
	// Try the --netbird-config flag, then <config dir>/netbird-config.json
	nbCfgPath := runAllNetbirdConfig
	if nbCfgPath == "" {
		nbCfgPath = filepath.Join(dataDir, "netbird-config.json")
	}
	fileCfg, err := netbird_integration.LoadConfigFile(nbCfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("netbird: no config file at ", nbCfgPath, ", using the environment only")
			return envCfg
		}
		log.Error("netbird: ", err, " — netbird is disabled until the config is fixed")
		return nil
	}
	fileCfg.DataDir = dataDir
	return fileCfg
}
