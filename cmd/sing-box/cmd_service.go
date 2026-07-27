//go:build with_netbird && windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

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

	// Wait for SCM signals in main goroutine
	// Engines are started by HTTP API call (from Flutter UI), not automatically.
	ctl := &nbControl{stopCh: d.stopCh, cfgPath: cfgPath}
	go ctl.serveHTTP()

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
		}
	}
	return false, 0
}

// ---- HTTP Control API (on 127.0.0.1:41732) ----

type nbControl struct {
	stopCh  chan struct{}
	cfgPath string
	mu      sync.Mutex
	running bool
	cleanup func()
}

func (c *nbControl) serveHTTP() {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.running {
			json.NewEncoder(w).Encode(map[string]string{"status": "already_running"})
			return
		}
		go func() {
			runAllEnableNetbird = true
			stopCh := make(chan struct{})
			cleanup, err := runAllEngines(c.cfgPath, stopCh)
			c.mu.Lock()
			if err != nil {
				log.Error("service: engine start failed: ", err)
				c.running = false
				c.mu.Unlock()
				return
			}
			c.cleanup = cleanup
			c.running = true
			c.mu.Unlock()
			log.Info("service: engines started via HTTP API")
			// The stopCh is not used here — engines are stopped via /stop endpoint
			// which calls cleanup. runAllEngines blocks until stopCh receives,
			// so we need to block here too to keep the goroutine alive.
			<-stopCh
		}()
		json.NewEncoder(w).Encode(map[string]string{"status": "starting"})
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cleanup != nil {
			c.cleanup()
			c.running = false
			c.cleanup = nil
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"running": running,
		})
	})
	server := &http.Server{Addr: "127.0.0.1:41732", Handler: mux}
	log.Info("service: HTTP control API on 127.0.0.1:41732")
	server.ListenAndServe()
}
