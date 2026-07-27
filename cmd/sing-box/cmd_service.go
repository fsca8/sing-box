//go:build with_netbird && windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sagernet/sing-box/log"
	"github.com/spf13/cobra"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const nbServiceName = "SingNetbird"

var commandService = &cobra.Command{
	Use:   "service",
	Short: "Manage Windows service (install/start/stop/remove)",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runServiceDaemon(); err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	// Subcommands
	commandService.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install and auto-start the service on boot",
		Run: func(cmd *cobra.Command, args []string) {
			if err := serviceInstall(); err != nil {
				log.Fatal(err)
			}
		},
	})
	commandService.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Remove the service",
		Run: func(cmd *cobra.Command, args []string) {
			if err := serviceRemove(); err != nil {
				log.Fatal(err)
			}
		},
	})
	commandService.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the service",
		Run: func(cmd *cobra.Command, args []string) {
			if err := serviceStart(); err != nil {
				log.Fatal(err)
			}
		},
	})
	commandService.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the service",
		Run: func(cmd *cobra.Command, args []string) {
			if err := serviceStop(); err != nil {
				log.Fatal(err)
			}
		},
	})
	mainCommand.AddCommand(commandService)
}

func serviceInstall() error {
	exe, _ := os.Executable()
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.CreateService(nbServiceName, exe, mgr.Config{
		DisplayName: "Sing-box Netbird Engine",
		Description: "Provides persistent netbird tunnel for sing-box proxy",
		StartType:   mgr.StartAutomatic,
	}, "service")
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	fmt.Printf("Service %q installed (auto-start on boot)\n", nbServiceName)
	fmt.Printf("  Binary: %s\n", exe)
	return nil
}

func serviceRemove() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(nbServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	fmt.Printf("Service %q removed\n", nbServiceName)
	return nil
}

func serviceStart() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(nbServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Printf("Service %q started\n", nbServiceName)
	return nil
}

func serviceStop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(nbServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	fmt.Printf("Service %q stopped (state=%d)\n", nbServiceName, status.State)
	return nil
}

// ---- Daemon (runs as Windows service) ----

func runServiceDaemon() error {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("check service: %w", err)
	}
	if !inService {
		return fmt.Errorf("not running as a service. Use: service install/start/stop/remove")
	}
	return svc.Run(nbServiceName, &nbDaemon{stopCh: make(chan struct{})})
}

// serviceDataDir returns a writable directory for netbird state.
// Falls back to the exe directory when configPaths is empty (service mode).
func serviceDataDir() string {
	if len(configPaths) > 0 {
		// configPaths might be []string{"config.json"} (default from cobra),
		// not an actual -c flag. Use exe dir instead for reliable path.
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	return os.TempDir()
}

type nbDaemon struct {
	stopCh chan struct{}
}

func (d *nbDaemon) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	// Log to a file that SYSTEM can definitely write to
	logPath := `C:\Windows\Temp\sing-box-service.log`
	logFile, err := os.Create(logPath)
	if err != nil {
		// Last resort: prepend to a marker file the user can see
		os.WriteFile(`C:\Windows\Temp\sing-box-service.err`, []byte(err.Error()+"\n"), 0644)
	}
	if logFile != nil {
		defer logFile.Close()
		fmt.Fprintf(logFile, "EXECUTE STARTED args=%v\n", args)
		logFile.Sync()
	}

	changes <- svc.Status{State: svc.StartPending}
	dataDir := serviceDataDir()
	cfgPath := filepath.Join(dataDir, "data", "sing-box-config.json")
	exePath, _ := os.Executable()

	if logFile != nil {
		fmt.Fprintf(logFile, "exe=%s dataDir=%s cfgPath=%s len(configPaths)=%d\n",
			exePath, dataDir, cfgPath, len(configPaths))
		logFile.Sync()
	}

	// Check if config exists
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		msg := fmt.Sprintf("no config at %s, exiting", cfgPath)
		if logFile != nil {
			fmt.Fprintln(logFile, msg)
			logFile.Sync()
		}
		log.Warn("service: ", msg)
		return false, 0
	}
	if logFile != nil {
		fmt.Fprintf(logFile, "config found at %s\n", cfgPath)
		logFile.Sync()
	}

	log.Info("service: config found, starting engines")
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	if logFile != nil {
		fmt.Fprintln(logFile, "Running state reported to SCM")
	}

	// Start engines in background
	type result struct {
		cleanup func()
		err     error
	}
	resCh := make(chan result, 1)
	go func() {
		if logFile != nil {
			fmt.Fprintln(logFile, "goroutine: starting runAllEngines")
		}
		runAllEnableNetbird = true
		cleanup, err := runAllEngines(cfgPath)
		if logFile != nil {
			if err != nil {
				fmt.Fprintf(logFile, "goroutine: runAllEngines error: %v\n", err)
			} else {
				fmt.Fprintf(logFile, "goroutine: runAllEngines returned cleanup=%v\n", cleanup != nil)
			}
		}
		resCh <- result{cleanup, err}
	}()

	// Wait for SCM stop signal or engine completion
	loop := true
	for loop {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				if logFile != nil {
					fmt.Fprintln(logFile, "SCM stop signal received")
				}
				loop = false
			}
		case res := <-resCh:
			if logFile != nil {
				if res.err != nil {
					fmt.Fprintf(logFile, "engine error: %v\n", res.err)
				} else {
					fmt.Fprintln(logFile, "engine startup complete (no error)")
				}
			}
			if res.err != nil {
				log.Error("service: engine error: ", res.err)
			} else if res.cleanup != nil {
				log.Warn("service: engines exited unexpectedly")
			}
			loop = false
		}
	}

	if logFile != nil {
		fmt.Fprintln(logFile, "stopping engines")
	}
	log.Info("service: stopping engines")
	select {
	case res := <-resCh:
		if res.cleanup != nil {
			res.cleanup()
			if logFile != nil {
				fmt.Fprintln(logFile, "cleanup called")
			}
		}
	default:
		if logFile != nil {
			fmt.Fprintln(logFile, "no cleanup needed (engines never started)")
		}
	}
	if logFile != nil {
		fmt.Fprintln(logFile, "EXIT OK")
		logFile.Sync()
	}
	return false, 0
}
