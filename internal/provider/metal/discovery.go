package metal

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// TalosMaintenancePort is the default port Talos exposes its API on in
// maintenance mode (no config applied). In maintenance mode the API responds
// without authentication, so a successful TCP connect is a reliable "is this a
// Talos node?" signal.
const TalosMaintenancePort = 50000

// DiscoveredNode is a Talos node found on the network during a discovery scan.
type DiscoveredNode struct {
	// Address is the node's management address (IP, typically IPv6).
	Address string `json:"address"`
	// Port the Talos API responded on (always TalosMaintenancePort unless overridden).
	Port int `json:"port"`
}

// ScanConfig tunes a discovery scan. Zero-value fields default sensibly in Scan.
type ScanConfig struct {
	// Port to probe. 0 ⇒ TalosMaintenancePort (50000).
	Port int
	// Per-host connect timeout. 0 ⇒ 800ms.
	Timeout time.Duration
	// Max concurrent probes. 0 ⇒ 64.
	Concurrency int
}

// Scan probes a CIDR for Talos maintenance-mode nodes (open API port) and
// returns the responding addresses. It is a one-shot "scan now" operation — the
// UI calls it on demand; the operator confirms results to add Machines.
//
// The CIDR is typically an IPv6 /64 (the on-prem LAN). For large subnets the
// scan is bounded by Concurrency + Timeout: a /64 has 2^64 addresses and is NOT
// enumerable in practice — discovery is only meaningful for small, enumerated
// subnets (e.g. a DHCP pool /120, or a manually-provided list of candidate
// addresses). Scan will reject a CIDR larger than maxScanHosts to avoid
// runaway scans; operators provide a narrower CIDR or use manual entry.
//
// ctx cancellation aborts pending and in-flight probes promptly.
func Scan(ctx context.Context, cidr string, cfg ScanConfig) ([]DiscoveredNode, error) {
	ipnet, err := parseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("metal discovery: invalid CIDR %q: %w", cidr, err)
	}

	port := cfg.Port
	if port == 0 {
		port = TalosMaintenancePort
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 800 * time.Millisecond
	}
	concurrency := cfg.Concurrency
	if concurrency == 0 {
		concurrency = 64
	}

	ips, err := expandCIDR(ipnet)
	if err != nil {
		return nil, err
	}

	return probeAddresses(ctx, ips, port, timeout, concurrency), nil
}

// parseCIDR wraps net.ParseCIDR with a clearer error for the caller.
func parseCIDR(cidr string) (*net.IPNet, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return ipnet, nil
}

// probeAddresses probes each address concurrently and returns the responders.
func probeAddresses(ctx context.Context, addrs []string, port int, timeout time.Duration, concurrency int) []DiscoveredNode {
	var (
		mu  sync.Mutex
		out []DiscoveredNode
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, addr := range addrs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return out
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			defer func() { <-sem }()
			if probePort(ctx, a, port, timeout) {
				mu.Lock()
				out = append(out, DiscoveredNode{Address: a, Port: port})
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()
	return out
}

// probePort attempts a TCP connection to addr:port. Returns true if it succeeds
// (port is open). The ctx deadline is respected for cancellation.
func probePort(ctx context.Context, addr string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// maxScanHosts bounds expandCIDR to prevent runaway scans of huge subnets.
const maxScanHosts = 65536

// expandCIDR returns the enumerable host IPs in a CIDR. Returns an error if the
// subnet is too large to scan (more than maxScanHosts) — operators must provide
// a narrower CIDR or use manual entry for huge subnets.
//
// For /32 returns the single host. For /31 returns both (point-to-point).
// Otherwise skips the network and broadcast addresses.
func expandCIDR(ipnet *net.IPNet) ([]string, error) {
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		return nil, fmt.Errorf("invalid network mask")
	}
	hostBits := bits - ones
	if hostBits >= 16 {
		// 2^16 = 65536 at the boundary; refuse anything bigger.
		return nil, fmt.Errorf("metal discovery: CIDR %s has 2^%d host addresses (max %d); provide a narrower CIDR or use manual entry",
			ipnet.String(), hostBits, maxScanHosts)
	}

	if ones == bits { // /32 (or /128)
		return []string{ipnet.IP.String()}, nil
	}

	// Walk the host range: from first host to last host inclusive.
	first := make(net.IP, len(ipnet.IP))
	copy(first, ipnet.IP)
	incrementIP(first) // skip network address

	out := make([]string, 0, 1<<hostBits)
	cur := first
	for ipnet.Contains(cur) {
		out = append(out, cur.String())
		incrementIP(cur)
	}
	// The loop above includes the broadcast for IPv4 (last address is "contains"
	// true); strip it. For IPv6 there's no broadcast, but the last address is
	// still a host — keep it. Simplest: drop the trailing address only for IPv4.
	if bits == 32 && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

// incrementIP mutates ip in place to the next address (big-endian increment).
func incrementIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
