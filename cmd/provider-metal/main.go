// Package main implements the provider-metal binary.
//
// provider-metal is a standalone RezusCloud infrastructure provider that
// discovers bare metal Talos machines on the local network and registers
// them with the management plane. It connects outbound to the RezusCloud
// REST API.
//
// Per ADR 12/13, the provider only creates and deletes machines. Config
// delivery uses SideroLink (pull model). For v1, the focus is on
// auto-discovery — scanning the subnet for Talos nodes in maintenance
// mode (gRPC API on port 50000).
//
// Future versions will add: IPMI/Redfish power control, PXE boot,
// Wake-on-LAN.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/rezuscloud/rezuscloud/internal/provider/metal"
)

func main() {
	configPath := flag.String("config", "provider-metal.yaml", "path to provider config file")
	apiURL := flag.String("api-url", os.Getenv("REZUSCLOUD_API_URL"), "RezusCloud management API URL")
	apiToken := flag.String("api-token", os.Getenv("REZUSCLOUD_API_TOKEN"), "RezusCloud API token")
	flag.Parse()

	if *apiURL == "" {
		*apiURL = "https://demo.rezus.cloud"
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("provider-metal starting...")

	cfg, err := metal.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	provider, err := metal.NewProvider(cfg)
	if err != nil {
		log.Fatalf("create provider: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := provider.Register(ctx, *apiURL, *apiToken); err != nil {
		log.Fatalf("register: %v", err)
	}

	fmt.Println("provider-metal registered. Starting discovery + heartbeat loop...")
	provider.Run(ctx)
	fmt.Println("shutting down")
}
