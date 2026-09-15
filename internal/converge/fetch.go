package converge

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	client "github.com/siderolabs/talos/pkg/machinery/client"
	"google.golang.org/grpc"
)

// currentNodeConfigPath is where Talos persists the active machine config
// (STATE partition mount + config filename — machinery constants).
const currentNodeConfigPath = "/system/state/config.yaml"

// CurrentNodeConfig fetches the machine's applied config from the node over
// the management tunnel (feeds the desired-vs-applied diff, ADR 0008). The
// node must be configured — a maintenance-mode node has no state config yet.
func (e *Engine) CurrentNodeConfig(ctx context.Context, machineID string) (string, error) {
	e.mu.Lock()
	addr := e.addrs[machineID]
	e.mu.Unlock()
	if addr == "" {
		return "", fmt.Errorf("machine %q is not connected to the management link", machineID)
	}
	m, err := e.store.GetMachine(machineID)
	if err != nil || m == nil {
		return "", fmt.Errorf("machine %q: %w", machineID, err)
	}
	tenant := m.Metadata.Labels["rezuscloud.io/tenant"]
	cc, err := e.tenantClientConfig(tenant)
	if err != nil {
		return "", fmt.Errorf("client credentials: %w", err)
	}

	host, _, err := splitHostPort(addr)
	if err != nil {
		return "", err
	}
	target := "passthrough:///" + net.JoinHostPort(host, strconv.Itoa(apidPort))
	dialer := grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return e.link.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(apidPort)))
	})

	c, err := client.New(ctx,
		client.WithConfig(cc),
		client.WithEndpoints(target),
		client.WithGRPCDialOptions(dialer),
	)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer c.Close() //nolint:errcheck

	rc, err := c.Read(ctx, currentNodeConfigPath)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	defer rc.Close() //nolint:errcheck
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	return string(data), nil
}
