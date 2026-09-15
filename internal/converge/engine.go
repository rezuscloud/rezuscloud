// Package converge implements ADR 0008's pull-model config delivery: the
// platform holds each machine's rendered Talos config (generation unchanged —
// the configrender pipeline) and converges a node to it whenever the node
// appears on the management link (ADR 0018) and whenever the desired config
// changes while the node is connected.
//
// The delivery wire is the node's Talos API (apid :1024) reached through the
// management tunnel: a booting node sits in maintenance mode (its bootstrap
// config is deliberately incomplete — ADR 0008) and accepts an unauthenticated
// maintenance apply; a configured node requires the tenant's Talos admin
// certificate (already part of the tenant secrets bundle). The node's
// provision-time join token is its binding token: the platform maps it to the
// TF-created machine record (declare-first tenant assignment), never trusting
// the runtime-generated WG key.
package converge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/configrender"
	"github.com/rezuscloud/rezuscloud/internal/mgmtlink"
	"github.com/rezuscloud/rezuscloud/internal/state"
	cryptox509 "github.com/siderolabs/crypto/x509"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	client "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"google.golang.org/grpc"
)

// apidPort is the Talos API port on the node (constants.ApisdPort).
const apidPort = 1024

// Engine converges connected machines to their rendered config.
type Engine struct {
	store state.StoreAPI
	link  *mgmtlink.Server
	log   *slog.Logger

	mu    sync.Mutex
	addrs map[string]string // machine ID → last-seen node tunnel address

	// Renderer produces the desired config for a machine. Defaults to
	// configrender.GenerateMachineConfig with the standard patch resolver.
	Renderer func(ctx context.Context, store state.StoreAPI, tenant, machineID string) (*configrender.MachineConfigResult, error)

	// DialTimeout bounds a single delivery attempt (default 3s): an attempt
	// fired before the node's WG session is up fails fast and is retried
	// (a connect event precedes the tunnel; see ConvergeBudget).
	DialTimeout time.Duration

	// ConvergeBudget bounds the retry loop around one connect event
	// (default 60s).
	ConvergeBudget time.Duration
}

// New wires the engine. Call SetPeerListener via Start, or use Run.
func New(store state.StoreAPI, link *mgmtlink.Server, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store: store,
		link:  link,
		log:   log,
		addrs: map[string]string{},
		Renderer: func(ctx context.Context, store state.StoreAPI, tenant, machineID string) (*configrender.MachineConfigResult, error) {
			return configrender.GenerateMachineConfig(ctx, store, store, patch.ResolvePatches,
				configrender.MachineConfigRequest{TenantName: tenant, MachineID: machineID})
		},
		DialTimeout:    3 * time.Second,
		ConvergeBudget: 60 * time.Second,
	}
}

// Run drives the engine until ctx is cancelled: peer lifecycle from the
// management link plus re-convergence on resource changes.
func (e *Engine) Run(ctx context.Context) {
	e.link.SetTokenAcceptor(func(token string) bool {
		m, err := e.store.FindMachineByBindingToken(token)
		return err == nil && m != nil
	})
	e.link.SetPeerListener(func(ev mgmtlink.PeerEvent) { e.onPeer(ctx, ev) })
	<-ctx.Done()
}

// ConvergeMachine re-delivers the machine's desired config over the tunnel.
// The connect-driven path is automatic; this is the entry point for
// change-driven convergence (config patch edits, tenant updates) — callers
// invoke it from their event source. No-op when the machine is not on the
// link.
func (e *Engine) ConvergeMachine(ctx context.Context, machineID string) error {
	e.mu.Lock()
	addr := e.addrs[machineID]
	e.mu.Unlock()
	if addr == "" {
		return fmt.Errorf("machine %q is not connected to the management link", machineID)
	}
	m, err := e.store.GetMachine(machineID)
	if err != nil || m == nil {
		return fmt.Errorf("machine %q: %w", machineID, err)
	}
	peerCtx, cancel := context.WithTimeout(ctx, e.ConvergeBudget)
	defer cancel()
	return e.convergeWithRetry(peerCtx, m, addr)
}

// onPeer reacts to the tunnel lifecycle. Only connect events trigger
// convergence; disconnects only mark the machine.
func (e *Engine) onPeer(ctx context.Context, ev mgmtlink.PeerEvent) {
	switch ev.Kind {
	case mgmtlink.PeerDisconnected:
		e.markDisconnected(ev.NodeAddrPort)
		return
	case mgmtlink.PeerConnected:
	default:
		return
	}

	m, err := e.resolveMachine(ev)
	if err != nil {
		e.log.Warn("mgmtlink node cannot be resolved to a machine record",
			"node_uuid", ev.NodeUUID, "err", err)
		return
	}

	e.mu.Lock()
	e.addrs[m.Metadata.Name] = ev.NodeAddrPort
	e.mu.Unlock()

	// Connected is spec-plane, but it is observed state of the link the
	// engine owns operationally (status plane rules: never authoritative,
	// never written to TF state — this is the store's link status only).
	e.setConnected(m.Metadata.Name, true)

	if err := e.convergeWithRetry(ctx, m, ev.NodeAddrPort); err != nil {
		e.log.Error("config convergence failed", "machine", m.Metadata.Name, "err", err)
	}
}

// convergeWithRetry drives convergence to a verdict: attempts are short
// (DialTimeout) so a connect event racing the WG session fails fast and
// retries until the tunnel carries traffic.
func (e *Engine) convergeWithRetry(ctx context.Context, m *state.Machine, nodeAddrPort string) error {
	deadline := time.Now().Add(e.ConvergeBudget)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, e.DialTimeout)
		err := e.converge(attemptCtx, m, nodeAddrPort)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		e.log.Debug("converge attempt failed; retrying", "machine", m.Metadata.Name, "err", err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("converge budget exhausted: %w", lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// resolveMachine maps a provision event to its machine record via the
// binding token (declare-first). Nodes without a record are refused —
// nothing to converge to.
func (e *Engine) resolveMachine(ev mgmtlink.PeerEvent) (*state.Machine, error) {
	m, err := e.store.FindMachineByBindingToken(ev.JoinToken)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("no machine record declares this binding token")
	}
	return m, nil
}

// converge renders the machine's desired config and applies it over the
// tunnel. Delivery is idempotent — re-running it on every connect and every
// config change is the reconcile.
func (e *Engine) converge(ctx context.Context, m *state.Machine, nodeAddrPort string) error {
	tenant := m.Metadata.Labels["rezuscloud.io/tenant"]
	if tenant == "" {
		return fmt.Errorf("machine %q has no tenant label", m.Metadata.Name)
	}

	res, err := e.Renderer(ctx, e.store, tenant, m.Metadata.Name)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}

	// Maintenance first: a bootstrapping node (incomplete config) serves an
	// unauthenticated maintenance API. A configured node needs the tenant's
	// admin certificate.
	if err := e.deliver(ctx, deliverRequest{
		NodeAddr:   nodeAddrPort,
		ConfigYAML: res.YAML,
	}); err != nil {
		// Fall back to the authenticated API (node already configured).
		creds, credErr := e.tenantClientConfig(tenant)
		if credErr != nil {
			return fmt.Errorf("deliver (maintenance): %w (no client credentials for authenticated retry: %w)", err, credErr)
		}
		e.log.Debug("maintenance apply failed; retrying authenticated", "machine", m.Metadata.Name, "err", err)
		if err := e.deliver(ctx, deliverRequest{
			NodeAddr:     nodeAddrPort,
			ConfigYAML:   res.YAML,
			ClientConfig: creds,
		}); err != nil {
			return fmt.Errorf("deliver (authenticated): %w", err)
		}
	}

	e.markConverged(m.Metadata.Name)
	e.log.Info("machine config converged", "machine", m.Metadata.Name,
		"tenant", tenant, "machine_type", string(res.MachineType))
	return nil
}

// tenantClientConfig mints the converge engine's Talos API client identity
// from the tenant secrets bundle: an os:admin client certificate signed by
// the tenant's OS CA — the same authority the tenant's talosconfig uses.
func (e *Engine) tenantClientConfig(tenant string) (*clientconfig.Config, error) {
	bundleJSON, err := e.store.LoadTenantSecrets(tenant)
	if err != nil {
		return nil, err
	}
	if bundleJSON == nil {
		return nil, fmt.Errorf("no secrets bundle for tenant %q", tenant)
	}
	bundle := &secrets.Bundle{}
	if err := json.Unmarshal(bundleJSON, bundle); err != nil {
		return nil, fmt.Errorf("parse secrets bundle: %w", err)
	}

	osCA, err := cryptox509.NewCertificateAuthorityFromCertificateAndKey(bundle.Certs.OS)
	if err != nil {
		return nil, fmt.Errorf("parse OS CA: %w", err)
	}
	clientPair, err := cryptox509.NewKeyPair(osCA,
		cryptox509.CommonName("rezuscloud-converge"),
		cryptox509.Organization("os:admin"),
		cryptox509.NotBefore(time.Now().Add(-10*time.Second)),
		cryptox509.NotAfter(time.Now().Add(365*24*time.Hour)),
		cryptox509.KeyUsage(x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment),
		cryptox509.ExtKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}),
	)
	if err != nil {
		return nil, fmt.Errorf("mint client certificate: %w", err)
	}

	return clientconfig.NewConfig("rezuscloud", nil, bundle.Certs.OS.Crt, cryptox509.NewCertificateAndKeyFromKeyPair(clientPair)), nil
}

func (e *Engine) setConnected(machineID string, connected bool) {
	m, err := e.store.GetMachine(machineID)
	if err != nil || m == nil {
		return
	}
	m.Spec.Connected = connected
	if _, err := e.store.UpdateMachineSpec(machineID, m.Metadata.ResourceVersion, m.Spec, m.Metadata.Labels, m.Metadata.Annotations); err != nil {
		e.log.Warn("machine connect flag not persisted", "machine", machineID, "err", err)
	}
}

// markConverged records a successful delivery. Stage advances only from
// configuring (the lifecycle stage this engine owns the exit of); other
// stages belong to their owners.
func (e *Engine) markConverged(machineID string) {
	m, err := e.store.GetMachine(machineID)
	if err != nil || m == nil {
		return
	}
	st := m.Status
	st.ConfigCurrent = true
	st.Maintenance = false
	if st.Stage == state.StageConfiguring {
		st.Stage = state.StageReady
	}
	if _, err := e.store.UpdateMachineStatus(machineID, st); err != nil {
		e.log.Warn("converged status not persisted", "machine", machineID, "err", err)
	}
}

// markDisconnected flags machines whose tunnel stream detached. Machines are
// matched on the ULA host of the stream address.
func (e *Engine) markDisconnected(nodeAddrPort string) {
	machines, _, err := e.store.ListMachines()
	if err != nil {
		return
	}
	for _, m := range machines {
		if m.Spec.Connected && m.Spec.ManagementAddress == nodeAddrPort {
			e.setConnected(m.Metadata.Name, false)
			return
		}
	}
}

// deliverRequest is one ApplyConfiguration attempt.
type deliverRequest struct {
	// NodeAddr is the node's tunnel address:port (host part is dialed; the
	// port is overridden with apidPort).
	NodeAddr string
	// ConfigYAML is the desired machine config.
	ConfigYAML string
	// ClientConfig authenticates against a configured node's apid. When
	// nil, the maintenance API is targeted (self-signed cert, skip-verify).
	ClientConfig *clientconfig.Config
}

// deliver routes Deliver through this engine's management link.
func (e *Engine) deliver(ctx context.Context, req deliverRequest) error {
	return Deliver(ctx, req, e.link.DialContext)
}

// Deliver applies cfg to the node's Talos API over dial (the management
// tunnel's DialContext). NO_REBOOT: config convergence must not bounce
// machines; stage changes that require a reboot are the upgrade engine's
// domain.
func Deliver(ctx context.Context, req deliverRequest, dial func(ctx context.Context, network, address string) (net.Conn, error)) error {
	host, _, err := splitHostPort(req.NodeAddr)
	if err != nil {
		return err
	}

	// passthrough:/// skips gRPC's default DNS resolver — the endpoint is
	// reached through the tunnel dialer, not name resolution.
	target := "passthrough:///" + net.JoinHostPort(host, strconv.Itoa(apidPort))
	dialer := grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(apidPort)))
	})

	var opts []client.OptionFunc
	opts = append(opts, client.WithGRPCDialOptions(dialer), client.WithEndpoints(target))
	if req.ClientConfig != nil {
		opts = append(opts, client.WithConfig(req.ClientConfig))
	} else {
		// Maintenance API: self-signed certificate.
		opts = append(opts, client.WithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	}

	c, err := client.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close() //nolint:errcheck

	_, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: []byte(req.ConfigYAML),
		Mode: machineapi.ApplyConfigurationRequest_NO_REBOOT,
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("apply timed out")
	}
	return err
}

// splitHostPort splits addr:port, returning the host.
func splitHostPort(addrPort string) (string, string, error) {
	return net.SplitHostPort(addrPort)
}
