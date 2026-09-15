package mgmtlink

import (
	"fmt"
	"net/netip"
	"strings"
)

// Config configures the management-link server. Sourced from
// REZUSCLOUD_MGMTLINK_* environment variables; absent = disabled, partial =
// startup error (the OIDC convention).
type Config struct {
	// Prefix is the ULA the server allocates node /64s from. Must be shorter
	// than /64 (65536 nodes at /48). The first /64 is reserved for the server.
	Prefix netip.Prefix
	// JoinToken is the enrollment secret nodes present at provisioning.
	JoinToken string
	// Listen is the gRPC address serving Provision + WireGuardOverGRPC
	// ("host:port"). Node kernel arg: siderolink.api=grpc://<host:port>?jointoken=...
	Listen string
	// WGListen is the UDP address for kernel-mode WireGuard transport
	// ("host:port"); empty disables UDP (tunnel-mode only).
	WGListen string
	// AdvertisedEndpoints are the WG endpoints handed to nodes in provision
	// responses ("host:port" each). Defaults to WGListen's local address;
	// set when the platform is behind NAT (nodes need the public endpoint).
	AdvertisedEndpoints []string
	// StreamPort is the port portion of a tunnel-mode node's virtual
	// address (cosmetic at the IP layer; the string keys the stream).
	StreamPort uint16
	// MTU for the tunnel. Talos uses 1280 (wireguard.LinkMTU) — keep equal.
	MTU int
}

// Enabled reports whether the server should be wired up.
func (c Config) Enabled() bool {
	return c.Prefix.IsValid() || c.JoinToken != "" || c.Listen != ""
}

// Validate rejects partial configuration loudly.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if !c.Prefix.IsValid() {
		return fmt.Errorf("REZUSCLOUD_MGMTLINK_PREFIX is required to enable the management link")
	}
	if c.Prefix.Bits() >= 64 {
		return fmt.Errorf("mgmtlink prefix must be shorter than /64, got %s", c.Prefix)
	}
	if c.JoinToken == "" {
		return fmt.Errorf("REZUSCLOUD_MGMTLINK_JOIN_TOKEN is required to enable the management link")
	}
	if c.Listen == "" {
		return fmt.Errorf("REZUSCLOUD_MGMTLINK_LISTEN is required to enable the management link")
	}
	if c.MTU == 0 {
		c.MTU = 1280
	}
	return nil
}

// FromEnv builds the config from REZUSCLOUD_MGMTLINK_* variables.
func FromEnv(get func(string) string) Config {
	cfg := Config{
		Prefix:              parsePrefix(get("REZUSCLOUD_MGMTLINK_PREFIX")),
		JoinToken:           get("REZUSCLOUD_MGMTLINK_JOIN_TOKEN"),
		Listen:              get("REZUSCLOUD_MGMTLINK_LISTEN"),
		WGListen:            get("REZUSCLOUD_MGMTLINK_WGLISTEN"),
		AdvertisedEndpoints: splitNonEmpty(get("REZUSCLOUD_MGMTLINK_WG_ENDPOINTS")),
		StreamPort:          51820,
		MTU:                 1280,
	}
	return cfg
}

func parsePrefix(s string) netip.Prefix {
	if s == "" {
		return netip.Prefix{}
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Prefix{}
	}
	return p
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
