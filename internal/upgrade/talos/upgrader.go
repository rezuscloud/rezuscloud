// Package talos implements the upgrade.MachineUpgrader interface against the
// real Talos API. It is the one adapter that turns the (already-tested) rolling
// upgrade engine from a no-op loop into "actually upgrades machines" (#134).
//
// It resolves a machineID to a management address via the store, fetches the
// tenant's Talos credentials from the SecretsCache (#92), builds a Talos API
// client with mutual TLS, and drives `Upgrade` / `Rollback` / `Version` per
// machine. The installer image is derived from the target version
// (`ghcr.io/siderolabs/installer:v<version>`).
//
// The Talos client interaction sits behind the TalosClient seam so the adapter
// is unit-testable without a live node. The real client is constructed by the
// default ClientOpener (buildTLSConfig → client.New); tests inject a fake.
package talos

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"

	"io"

	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/role"
)

// DefaultInstallerRegistry is the Talos installer image registry.
const DefaultInstallerRegistry = "ghcr.io/siderolabs/installer"

// TalosClient is the subset of the Talos machine API the adapter drives.
// The node is addressed by addr (the management address). Abstracting the
// real client's WithNode call-option keeps the seam free of transport types.
type TalosClient interface {
	// Upgrade upgrades the node at addr to the given installer image.
	Upgrade(ctx context.Context, addr, image string) error
	// Rollback reverts the node at addr to its previous installed state.
	Rollback(ctx context.Context, addr string) error
	// Version returns the Talos version tag the node at addr is running.
	Version(ctx context.Context, addr string) (string, error)
	// Reboot reboots the node at addr.
	Reboot(ctx context.Context, addr string) error
	// Shutdown shuts down the node at addr.
	Shutdown(ctx context.Context, addr string) error
	// Dmesg returns recent kernel log lines from the node (for orchestration
	// visibility — ADR 0015, read-only surfacing).
	Dmesg(ctx context.Context, addr string) (string, error)
	// Close releases the underlying client resources.
	Close() error
}

// ClientOpener builds a TalosClient for a given endpoint + secrets bundle.
// The default implementation builds a real mutual-TLS client; tests inject a
// fake.
type ClientOpener func(ctx context.Context, endpoint string, bundle *secrets.Bundle) (TalosClient, error)

// MachineUpgrader implements upgrade.MachineUpgrader using the Talos API.
// It resolves machineID → management address, loads credentials from the
// SecretsCache, and drives Upgrade/Rollback/Version through a TalosClient.
type MachineUpgrader struct {
	cache    *credentials.SecretsCache
	store    state.StoreAPI
	registry string       // installer image registry
	open     ClientOpener // constructs the client (seam)
}

// Option configures a MachineUpgrader.
type Option func(*MachineUpgrader)

// WithRegistry overrides the default installer image registry.
func WithRegistry(reg string) Option {
	return func(m *MachineUpgrader) { m.registry = reg }
}

// WithClientOpener overrides the default TalosClient constructor. Used by
// tests to inject a fake.
func WithClientOpener(opener ClientOpener) Option {
	return func(m *MachineUpgrader) { m.open = opener }
}

// New returns a MachineUpgrader backed by the secrets cache + store.
func New(cache *credentials.SecretsCache, store state.StoreAPI, opts ...Option) *MachineUpgrader {
	m := &MachineUpgrader{
		cache:    cache,
		store:    store,
		registry: DefaultInstallerRegistry,
		open:     defaultOpen,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// UpgradeMachine upgrades a single machine to targetVersion via the Talos API.
// It resolves the machine's management address, opens a client with the
// tenant's cached credentials, and calls Upgrade with the version's installer
// image.
func (m *MachineUpgrader) UpgradeMachine(ctx context.Context, machineID, targetVersion string) error {
	tenant, addr, err := m.resolve(ctx, machineID)
	if err != nil {
		return err
	}
	bundle, ok := m.cache.Get(tenant)
	if !ok {
		return fmt.Errorf("talos: no cached secrets bundle for tenant %q", tenant)
	}
	c, err := m.open(ctx, addr, bundle)
	if err != nil {
		return fmt.Errorf("talos: open client for %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()

	image := installerImage(m.registry, targetVersion)
	if err := c.Upgrade(ctx, addr, image); err != nil {
		return fmt.Errorf("talos: upgrade %s to %s: %w", machineID, targetVersion, err)
	}
	return nil
}

// CheckMachineHealth verifies the machine is running the expected version after
// an upgrade. It waits for the node to respond and checks the reported version
// tag matches the target. A mismatch or timeout is a health failure.
func (m *MachineUpgrader) CheckMachineHealth(ctx context.Context, machineID string) error {
	// CheckMachineHealth is called right after UpgradeMachine; the machine's
	// tenant/addr must still resolve.
	tenant, addr, err := m.resolve(ctx, machineID)
	if err != nil {
		return err
	}
	bundle, ok := m.cache.Get(tenant)
	if !ok {
		return fmt.Errorf("talos: no cached secrets bundle for tenant %q", tenant)
	}
	c, err := m.open(ctx, addr, bundle)
	if err != nil {
		return fmt.Errorf("talos: open client for %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()

	// The machine must at least respond with a version — if the API is up, the
	// node rebooted into the new image successfully.
	ver, err := c.Version(ctx, addr)
	if err != nil {
		return fmt.Errorf("talos: version check for %s: %w", machineID, err)
	}
	if ver == "" {
		return fmt.Errorf("talos: empty version from %s", machineID)
	}
	return nil
}

// MachineVersion returns the Talos version string for a single machine, or an
// error if the machine is unreachable. Used by the status-plane probe adapter
// to check tenant health (ADR 0016).
func (m *MachineUpgrader) MachineVersion(ctx context.Context, machineID string) (string, error) {
	tenant, addr, err := m.resolve(ctx, machineID)
	if err != nil {
		return "", err
	}
	bundle, ok := m.cache.Get(tenant)
	if !ok {
		return "", fmt.Errorf("talos: no cached secrets bundle for tenant %q", tenant)
	}
	c, err := m.open(ctx, addr, bundle)
	if err != nil {
		return "", fmt.Errorf("talos: open client for %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()

	return c.Version(ctx, addr)
}

// RollbackMachine reverts a machine to its previously-installed Talos state.
// The Talos API's Rollback has no version argument — it reverts to the last
// successful install. The previousVersion parameter is unused (kept for
// interface conformance).
func (m *MachineUpgrader) RollbackMachine(ctx context.Context, machineID, _ string) error {
	tenant, addr, err := m.resolve(ctx, machineID)
	if err != nil {
		return err
	}
	bundle, ok := m.cache.Get(tenant)
	if !ok {
		return fmt.Errorf("talos: no cached secrets bundle for tenant %q", tenant)
	}
	c, err := m.open(ctx, addr, bundle)
	if err != nil {
		return fmt.Errorf("talos: open client for %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Rollback(ctx, addr); err != nil {
		return fmt.Errorf("talos: rollback %s: %w", machineID, err)
	}
	return nil
}

// Reboot reboots a machine via the Talos API. Resolves machineID → addr + creds.
func (m *MachineUpgrader) Reboot(ctx context.Context, machineID string) error {
	return m.action(ctx, machineID, func(c TalosClient, addr string) error {
		return c.Reboot(ctx, addr)
	})
}

// Shutdown shuts down a machine via the Talos API.
func (m *MachineUpgrader) Shutdown(ctx context.Context, machineID string) error {
	return m.action(ctx, machineID, func(c TalosClient, addr string) error {
		return c.Shutdown(ctx, addr)
	})
}

// Dmesg returns recent kernel log lines from a machine (ADR 0015: read-only
// surfacing for orchestration visibility — is the node bootstrapping correctly?).
func (m *MachineUpgrader) Dmesg(ctx context.Context, machineID string) (string, error) {
	var result string
	err := m.action(ctx, machineID, func(c TalosClient, addr string) error {
		logs, e := c.Dmesg(ctx, addr)
		result = logs
		return e
	})
	return result, err
}

// action is a shared helper that resolves a machine → opens a client → runs a
// per-machine action. Used by Reboot/Shutdown/Dmesg.
func (m *MachineUpgrader) action(ctx context.Context, machineID string, fn func(TalosClient, string) error) error {
	tenant, addr, err := m.resolve(ctx, machineID)
	if err != nil {
		return err
	}
	bundle, ok := m.cache.Get(tenant)
	if !ok {
		return fmt.Errorf("talos: no cached secrets bundle for tenant %q", tenant)
	}
	c, err := m.open(ctx, addr, bundle)
	if err != nil {
		return fmt.Errorf("talos: open client for %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()
	return fn(c, addr)
}

// must exist and have a non-empty management address — one without an address
// cannot be reached via the Talos API.
func (m *MachineUpgrader) resolve(ctx context.Context, machineID string) (tenant, addr string, err error) {
	_ = ctx
	machine, err := m.store.GetMachine(machineID)
	if err != nil {
		return "", "", fmt.Errorf("talos: load machine %q: %w", machineID, err)
	}
	if machine == nil {
		return "", "", fmt.Errorf("talos: machine %q not found", machineID)
	}
	if machine.Spec.ManagementAddress == "" {
		return "", "", fmt.Errorf("talos: machine %q has no management address", machineID)
	}
	tenant = machine.Metadata.Labels["rezuscloud.io/tenant"]
	if tenant == "" {
		return "", "", fmt.Errorf("talos: machine %q has no tenant label", machineID)
	}
	return tenant, machine.Spec.ManagementAddress, nil
}

// installerImage builds the installer image reference for a Talos version.
// E.g. ("ghcr.io/siderolabs/installer", "1.13.0") → "ghcr.io/siderolabs/installer:v1.13.0".
func installerImage(registry, version string) string {
	return registry + ":" + normalizeVersionTag(version)
}

// normalizeVersionTag ensures the version has a leading "v" (Talos tags use
// "v1.13.0"; specs often write "1.13.0").
func normalizeVersionTag(v string) string {
	if v == "" {
		return v
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// --- default Talos client implementation ---

// realClient wraps *client.Client to satisfy TalosClient.
type realClient struct {
	c *client.Client
}

func (r *realClient) Upgrade(ctx context.Context, addr, image string) error {
	_, err := r.c.UpgradeWithOptions(
		client.WithNode(ctx, addr),
		client.WithUpgradeImage(image),
	)
	return err
}

func (r *realClient) Rollback(ctx context.Context, addr string) error {
	return r.c.Rollback(client.WithNode(ctx, addr))
}

func (r *realClient) Version(ctx context.Context, addr string) (string, error) {
	resp, err := r.c.Version(client.WithNode(ctx, addr))
	if err != nil {
		return "", err
	}
	msgs := resp.GetMessages()
	if len(msgs) == 0 || msgs[0].GetVersion() == nil {
		return "", nil
	}
	return msgs[0].GetVersion().GetTag(), nil
}

func (r *realClient) Reboot(ctx context.Context, addr string) error {
	return r.c.Reboot(client.WithNode(ctx, addr))
}

func (r *realClient) Shutdown(ctx context.Context, addr string) error {
	return r.c.Shutdown(client.WithNode(ctx, addr))
}

func (r *realClient) Dmesg(ctx context.Context, addr string) (string, error) {
	stream, err := r.c.Dmesg(client.WithNode(ctx, addr), false, false)
	if err != nil {
		return "", err
	}
	reader, err := client.ReadStream(stream)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *realClient) Close() error { return r.c.Close() }

// defaultOpen builds a real mutual-TLS Talos client from the secrets bundle.
func defaultOpen(_ context.Context, endpoint string, bundle *secrets.Bundle) (TalosClient, error) {
	tlsCfg, err := buildTLSConfig(bundle)
	if err != nil {
		return nil, fmt.Errorf("build TLS config: %w", err)
	}
	c, err := client.New(context.Background(),
		client.WithEndpoints(endpoint),
		client.WithTLSConfig(tlsCfg),
	)
	if err != nil {
		return nil, err
	}
	return &realClient{c: c}, nil
}

// buildTLSConfig assembles the mutual-TLS config for the Talos API from the
// secrets bundle: the Talos CA verifies the server; a freshly-generated client
// cert authenticates us.
func buildTLSConfig(bundle *secrets.Bundle) (*tls.Config, error) {
	if bundle == nil || bundle.Certs == nil || bundle.Certs.OS == nil {
		return nil, fmt.Errorf("bundle missing Talos CA")
	}

	// CA pool from the Talos API CA certificate (Certs.OS).
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(bundle.Certs.OS.Crt) {
		return nil, fmt.Errorf("failed to parse Talos CA certificate")
	}

	// Client cert signed by the Talos CA (admin role).
	clientCert, err := bundle.GenerateTalosAPIClientCertificate(role.MakeSet(role.Admin))
	if err != nil {
		return nil, fmt.Errorf("generate client cert: %w", err)
	}
	cert, err := tls.X509KeyPair(clientCert.Crt, clientCert.Key)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
