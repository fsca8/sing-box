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

var (
	commandService = &cobra.Command{
		Use:   "service",
		Short: "Manage Windows service (install/start/stop/remove)",
		Run: func(cmd *cobra.Command, args []string) {
			if err := runServiceDaemon(); err != nil {
				log.Fatal(err)
			}
		},
	}
	serviceStartDir string // --config-dir flag value for "service start"
)

func init() {
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
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the service",
		Run: func(cmd *cobra.Command, args []string) {
			if err := serviceStart(); err != nil {
				log.Fatal(err)
			}
		},
	}
	startCmd.Flags().StringVar(&serviceStartDir, "config-dir", "", "config directory path (passed from Flutter)")
	commandService.AddCommand(startCmd)
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
	var startArgs []string
	if serviceStartDir != "" {
		startArgs = append(startArgs, "--config-dir", serviceStartDir)
	}
	if err := s.Start(startArgs...); err != nil {
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
	return svc.Run(nbServiceName, &nbDaemon{})
}

// serviceDataDir returns the config directory.
// Priority:
// 1. --config-dir from SCM start args (passed by Flutter "service start")
// 2. Fallback to {exeDir}\data\
func serviceDataDir() string {
	if exeDir != "" {
		return filepath.Join(exeDir, "data")
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), "data")
	}
	return filepath.Join(os.TempDir(), "data")
}

type nbDaemon struct{}

func (d *nbDaemon) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	// Read config dir from SCM start arguments (passed by "service start --config-dir")
	dataDir := serviceDataDir()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--config-dir" {
			if d := args[i+1]; d != "" {
				dataDir = d
			}
			break
		}
	}
	cfgPath := filepath.Join(dataDir, "sing-box-config.json")

	// Check if config exists
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		log.Warn("service: no config at ", cfgPath, ", exiting")
		return false, 0
	}

	log.Info("service: config found, starting engines")
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	// Start engines in background goroutine (non-blocking)
	var engCleanup func()
	go func() {
		runAllEnableNetbird = true
		cleanup, err := runAllEngines(cfgPath)
		if err != nil {
			log.Error("service: engine start failed: ", err)
		} else if cleanup != nil {
			engCleanup = cleanup
			log.Info("service: engines started")
		}
	}()

	// Wait for SCM stop signal
	for {
		c := <-r
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			log.Info("service: stopping engines")
			if engCleanup != nil {
				engCleanup()
			}
			return false, 0
		}
	}
}
