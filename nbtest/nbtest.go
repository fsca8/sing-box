//go:build with_netbird

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	nbembed "github.com/netbirdio/netbird/client/embed"
)

func main() {
	opts := nbembed.Options{
		SetupKey:      os.Getenv("NB_SETUP_KEY"),
		ManagementURL: os.Getenv("NB_MANAGEMENT_URL"),
		DeviceName:    "nb-test-client",
		LogLevel:      "info",
	}

	client, err := nbembed.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "New error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Start error: %v\n", err)
		os.Exit(1)
	}
	defer client.Stop(ctx)

	fmt.Println("Embed client started, waiting for tunnel...")
	time.Sleep(20 * time.Second)

	// Test: DialContext to netbird internal IP
	fmt.Println("\n--- DialContext to 100.121.254.26:443 ---")
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := client.DialContext(dctx, "tcp", "100.121.254.26:443")
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  FAILED: %v\n", err)
	} else {
		fmt.Println("  SUCCESS")
		conn.Close()
	}

	fmt.Println("\nDone.")
}
