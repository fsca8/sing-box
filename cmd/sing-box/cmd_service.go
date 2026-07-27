//go:build with_netbird && windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/experimental/netbird_integration"
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
		return filepath.Dir(configPaths[0])
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
	changes <- svc.Status{State: svc.StartPending}
	log.Info("netbird service: starting engines")

	// Determine config path from data dir (passed via serviceDataDir())
	dataDir := serviceDataDir()
	cfgPath := filepath.Join(dataDir, "sing-box-config.json")

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Info("netbird service: reported Running to SCM")

	// Start engines in background goroutine
	type engResult struct {
		cleanup func()
		err     error
	}
	resultCh := make(chan engResult, 1)
	go func() {
		runAllEnableNetbird = true
		cleanup, err := runAllEngines(cfgPath, d.stopCh)
		resultCh <- engResult{cleanup, err}
	}()

	// Wait for SCM signals in main goroutine
	loop := true
	for loop {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				loop = false
			}
		case res := <-resultCh:
			// runAllEngines finished (config missing or error)
			if res.err != nil {
				log.Error("netbird service: engine error: ", res.err)
			} else if res.cleanup != nil {
				// Shouldn't happen — runAllEngines only returns after stopCh
				log.Warn("netbird service: unexpected engine exit")
			}
			// No config yet — keep running idle
			if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
				log.Info("netbird service: no config yet, idle waiting")
				// Re-enter idle loop
				go func() {
					// Poll for config file
					for {
						select {
						case <-d.stopCh:
							return
						case <-time.After(5 * time.Second):
							if _, err := os.Stat(cfgPath); err == nil {
								runAllEnableNetbird = true
								cleanup, err := runAllEngines(cfgPath, d.stopCh)
								resultCh <- engResult{cleanup, err}
								return
							}
						}
					}
				}()
			}
		}
	}

	// Stop signal received — signal engines
	close(d.stopCh)
	log.Info("netbird service: stopping engines")
	// Wait for resultCh if engines started
	select {
	case res := <-resultCh:
		if res.cleanup != nil {
			res.cleanup()
		}
	case <-time.After(10 * time.Second):
		log.Warn("netbird service: force stop after timeout")
	}
	return false, 0
}

// ---- HTTP Control API (on 127.0.0.1:41732) ----

type nbControl struct {
	engine *netbird_integration.Engine
	stopCh chan struct{}
}

func (c *nbControl) serve() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"running": c.engine.IsRunning(),
		})
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "stopping"})
		close(c.stopCh)
	})
	server := &http.Server{Addr: "127.0.0.1:41732", Handler: mux}
	server.ListenAndServe()
}
