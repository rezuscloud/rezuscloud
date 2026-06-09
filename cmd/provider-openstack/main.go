// Package main implements the provider-openstack binary.
//
// provider-openstack is a standalone RezusCloud infrastructure provider that
// provisions virtual machines on OpenStack (Nova) via the Gophercloud SDK.
// It connects outbound to the RezusCloud management plane's REST API,
// registers its capabilities, and handles Provision/Destroy requests.
//
// Per ADR 12/13, the provider only creates and deletes machines. Config
// delivery uses SideroLink (pull model) — the provider never pushes Talos
// config.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/provider/openstack"
)

func main() {
	configPath := flag.String("config", "provider-openstack.yaml", "Path to configuration file")
	apiURL := flag.String("api-url", "", "RezusCloud API URL (e.g. https://demo.rezus.cloud)")
	apiToken := flag.String("api-token", "", "RezusCloud API token for authentication")
	flag.Parse()

	if *apiURL == "" {
		*apiURL = os.Getenv("REZUSCLOUD_API_URL")
	}
	if *apiToken == "" {
		*apiToken = os.Getenv("REZUSCLOUD_API_TOKEN")
	}
	if *apiURL == "" || *apiToken == "" {
		log.Fatal("REZUSCLOUD_API_URL and REZUSCLOUD_API_TOKEN (or flags) are required")
	}

	cfg, err := openstack.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	p, err := openstack.NewProvider(cfg)
	if err != nil {
		log.Fatalf("create provider: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Register with management plane.
	if err := p.Register(ctx, *apiURL, *apiToken); err != nil {
		log.Fatalf("register: %v", err)
	}
	log.Printf("registered as provider %q with %s", cfg.ProviderType, *apiURL)

	// Heartbeat loop.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.Heartbeat(ctx); err != nil {
					log.Printf("heartbeat failed: %v", err)
				}
			}
		}
	}()

	// Poll for provisioning requests.
	log.Println("provider-openstack running, polling for requests...")
	p.Run(ctx)
	fmt.Println("shutting down")
}
