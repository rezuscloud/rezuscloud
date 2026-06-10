package metal

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDiscoveryScannerProbeSubnet(t *testing.T) {
	// Start a fake Talos listener on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	// Accept connections in background.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	cfg := &Config{
		Discovery: DiscoveryConfig{
			Subnet:         "127.0.0.1/32",
			Port:           port,
			TimeoutSeconds: 1,
			Concurrency:    10,
		},
	}

	scanner := NewDiscoveryScanner(cfg)
	ctx := context.Background()
	cidr, _ := cfg.ParseCIDR()

	found := scanner.probeSubnet(ctx, cidr, cfg.Discovery.Port, 1*time.Second, 10)
	if len(found) == 0 {
		t.Error("expected to find 127.0.0.1 with open port")
	}
	if !found["127.0.0.1"] {
		t.Error("expected 127.0.0.1 in found set")
	}
}

func TestDiscoveryScannerNoOpenPort(t *testing.T) {
	cfg := &Config{
		Discovery: DiscoveryConfig{
			Subnet:         "127.0.0.1/32",
			Port:           59999, // unlikely to be open
			TimeoutSeconds: 1,
			Concurrency:    10,
		},
	}

	scanner := NewDiscoveryScanner(cfg)
	ctx := context.Background()
	cidr, _ := cfg.ParseCIDR()

	found := scanner.probeSubnet(ctx, cidr, 59999, 200*time.Millisecond, 10)
	if len(found) != 0 {
		t.Errorf("expected no hosts found, got %d", len(found))
	}
}

func TestDiscoveryScannerCallbacks(t *testing.T) {
	cfg := &Config{
		Discovery: DiscoveryConfig{
			Enabled:         true,
			Subnet:          "127.0.0.1/32",
			Port:            50000,
			IntervalSeconds: 1,
			TimeoutSeconds:  1,
			Concurrency:     10,
		},
	}

	scanner := NewDiscoveryScanner(cfg)

	discovered := make(chan string, 1)
	scanner.OnDiscover(func(m *DiscoveredMachine) {
		discovered <- m.IP
	})

	// Known should be empty initially.
	if machines := scanner.Known(); len(machines) != 0 {
		t.Errorf("expected 0 known machines, got %d", len(machines))
	}

	// Add a known machine manually.
	scanner.mu.Lock()
	scanner.known["192.168.7.100"] = &DiscoveredMachine{
		IP:       "192.168.7.100",
		Port:     50000,
		FoundAt:  time.Now(),
		LastSeen: time.Now(),
	}
	scanner.mu.Unlock()

	if machines := scanner.Known(); len(machines) != 1 {
		t.Errorf("expected 1 known machine, got %d", len(machines))
	}
}

func TestExpandCIDR(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("192.168.7.0/30")
	ips := expandCIDR(cidr)

	// /30 has 4 addresses: network (0), 2 hosts (1,2), broadcast (3).
	// We expect only the 2 host addresses.
	if len(ips) != 2 {
		t.Fatalf("expected 2 IPs for /30, got %d: %v", len(ips), ips)
	}
	if ips[0] != "192.168.7.1" {
		t.Errorf("expected 192.168.7.1, got %s", ips[0])
	}
	if ips[1] != "192.168.7.2" {
		t.Errorf("expected 192.168.7.2, got %s", ips[1])
	}
}

func TestExpandCIDR24(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/24")
	ips := expandCIDR(cidr)

	// /24 = 256 addresses, minus network (10.0.0.0) and broadcast (10.0.0.255) = 254 hosts.
	if len(ips) != 254 {
		t.Errorf("expected 254 IPs for /24, got %d", len(ips))
	}

	// First host should be .1, last should be .254.
	if ips[0] != "10.0.0.1" {
		t.Errorf("expected first IP 10.0.0.1, got %s", ips[0])
	}
	if ips[len(ips)-1] != "10.0.0.254" {
		t.Errorf("expected last IP 10.0.0.254, got %s", ips[len(ips)-1])
	}
}

func TestProbePort(t *testing.T) {
	// Start a listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	ctx := context.Background()

	if !probePort(ctx, "127.0.0.1", port, 1*time.Second) {
		t.Error("expected port to be open")
	}
	if probePort(ctx, "127.0.0.1", 59999, 200*time.Millisecond) {
		t.Error("expected port to be closed")
	}
}
