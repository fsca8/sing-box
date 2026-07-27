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
	dataDir := serviceDataDir()
	cfgPath := filepath.Join(dataDir, "sing-box-config.json")

	// Check if config exists
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Warn("service: no config at ", cfgPath, ", exiting")
		return false, 0 // clean exit, SCM will not restart
	}

	log.Info("service: config found, starting engines")
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Info("service: reported Running to SCM")

	// Start engines in background (netbird cold start takes ~48s)
	type result struct {
		cleanup func()
		err     error
	}
	resCh := make(chan result, 1)
	go func() {
		runAllEnableNetbird = true
		cleanup, err := runAllEngines(cfgPath)
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
				loop = false
			}
		case res := <-resCh:
			// Engine finished (error or unexpected exit)
			if res.err != nil {
				log.Error("service: engine error: ", res.err)
			} else if res.cleanup != nil {
				log.Warn("service: engines exited unexpectedly")
			}
			loop = false
		}
	}

	// Stop engines
	close(d.stopCh) // signal runAllEngines if still running
	log.Info("service: stopping engines")
	select {
	case res := <-resCh:
		if res.cleanup != nil {
			res.cleanup()
		}
	default:
	}
	return false, 0
}
