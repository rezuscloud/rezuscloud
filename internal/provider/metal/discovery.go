package metal

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// DiscoveredMachine represents a Talos node found on the network.
type DiscoveredMachine struct {
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	FoundAt     time.Time `json:"foundAt"`
	LastSeen    time.Time `json:"lastSeen"`
	Maintenance bool      `json:"maintenance"`
}

// DiscoveryScanner scans a subnet for Talos nodes in maintenance mode.
// Talos exposes a gRPC API on port 50000. In maintenance mode (no config
// applied), the API responds without authentication. We probe by attempting
// a TCP connection — if port 50000 is open and responds, it's a Talos node.
type DiscoveryScanner struct {
	cfg        *Config
	known      map[string]*DiscoveredMachine // IP -> machine
	mu         sync.RWMutex
	onDiscover func(*DiscoveredMachine) // callback when new machine found
	onLost     func(string)             // callback when machine disappears
}

// NewDiscoveryScanner creates a new subnet scanner.
func NewDiscoveryScanner(cfg *Config) *DiscoveryScanner {
	return &DiscoveryScanner{
		cfg:   cfg,
		known: make(map[string]*DiscoveredMachine),
	}
}

// OnDiscover sets the callback for newly discovered machines.
func (s *DiscoveryScanner) OnDiscover(fn func(*DiscoveredMachine)) {
	s.onDiscover = fn
}

// OnLost sets the callback for machines that disappeared.
func (s *DiscoveryScanner) OnLost(fn func(string)) {
	s.onLost = fn
}

// Known returns the currently known discovered machines.
func (s *DiscoveryScanner) Known() []*DiscoveredMachine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*DiscoveredMachine, 0, len(s.known))
	for _, m := range s.known {
		out = append(out, m)
	}
	return out
}

// Run starts the periodic scan loop. Blocks until context is cancelled.
func (s *DiscoveryScanner) Run(ctx context.Context) {
	interval := time.Duration(s.cfg.Discovery.IntervalSeconds) * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run initial scan immediately.
	s.scan(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

// scan performs a single scan of the configured subnet.
func (s *DiscoveryScanner) scan(ctx context.Context) {
	cidr, err := s.cfg.ParseCIDR()
	if err != nil {
		log.Printf("discovery: subnet parse error: %v", err)
		return
	}

	timeout := time.Duration(s.cfg.Discovery.TimeoutSeconds) * time.Second
	port := s.cfg.Discovery.Port
	concurrency := s.cfg.Discovery.Concurrency

	found := s.probeSubnet(ctx, cidr, port, timeout, concurrency)

	now := time.Now()
	s.mu.Lock()

	// Mark new discoveries.
	for ip := range found {
		if existing, ok := s.known[ip]; ok {
			existing.LastSeen = now
		} else {
			m := &DiscoveredMachine{
				IP:          ip,
				Port:        port,
				FoundAt:     now,
				LastSeen:    now,
				Maintenance: true, // We only find maintenance-mode nodes
			}
			s.known[ip] = m
			log.Printf("discovery: NEW machine at %s:%d", ip, port)
			if s.onDiscover != nil {
				// Copy to avoid holding lock during callback.
				go s.onDiscover(m)
			}
		}
	}

	// Check for lost machines (not seen in 3x interval).
	staleThreshold := time.Duration(s.cfg.Discovery.IntervalSeconds*3) * time.Second
	for ip, m := range s.known {
		if now.Sub(m.LastSeen) > staleThreshold {
			log.Printf("discovery: LOST machine at %s (last seen %v ago)", ip, now.Sub(m.LastSeen))
			delete(s.known, ip)
			if s.onLost != nil {
				go s.onLost(ip)
			}
		}
	}

	s.mu.Unlock()

	log.Printf("discovery: scan complete, %d machines found", len(found))
}

// probeSubnet probes all IPs in a CIDR for an open Talos API port.
// Returns a set of responding IPs.
func (s *DiscoveryScanner) probeSubnet(ctx context.Context, cidr *net.IPNet, port int, timeout time.Duration, concurrency int) map[string]bool {
	// Generate all IPs in the subnet.
	ips := expandCIDR(cidr)

	found := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrency with a semaphore.
	sem := make(chan struct{}, concurrency)

	for _, ip := range ips {
		select {
		case <-ctx.Done():
			wg.Wait()
			return found
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()

			if probePort(ctx, addr, port, timeout) {
				mu.Lock()
				found[addr] = true
				mu.Unlock()
			}
		}(ip)
	}

	wg.Wait()
	return found
}

// probePort attempts a TCP connection to addr:port with the given timeout.
// Returns true if the connection succeeds (port is open).
func probePort(ctx context.Context, addr string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", addr, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// expandCIDR returns all host IPs in a CIDR.
// For /32, returns the single IP. For /31, returns both IPs.
// Otherwise skips network and broadcast addresses.
func expandCIDR(cidr *net.IPNet) []string {
	ips := []string{}

	// Special case: /32 — single host, return it.
	ones, _ := cidr.Mask.Size()
	if ones == 32 {
		return []string{cidr.IP.String()}
	}

	// Special case: /31 — point-to-point, both are usable.
	if ones == 31 {
		ip1 := make(net.IP, len(cidr.IP))
		copy(ip1, cidr.IP)
		ip2 := make(net.IP, len(cidr.IP))
		copy(ip2, cidr.IP)
		incrementIP(ip2)
		return []string{ip1.String(), ip2.String()}
	}

	// General case: skip network (all zeros) and broadcast (all ones).
	ip := make(net.IP, len(cidr.IP))
	copy(ip, cidr.IP)

	// Start from first host (network + 1).
	incrementIP(ip)

	for {
		if !cidr.Contains(ip) {
			break
		}
		if !isBroadcast(ip, cidr) {
			ips = append(ips, ip.String())
		}
		incrementIP(ip)
	}
	return ips
}

func isBroadcast(ip net.IP, cidr *net.IPNet) bool {
	// For IPv4, the broadcast is the last address in the subnet.
	// Calculate it: network | ^mask
	broadcast := make(net.IP, len(cidr.IP))
	for i := range cidr.IP {
		broadcast[i] = cidr.IP[i] | ^cidr.Mask[i]
	}
	return ip.Equal(broadcast)
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] != 0 {
			break
		}
	}
}
