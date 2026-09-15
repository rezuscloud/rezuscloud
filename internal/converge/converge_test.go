package converge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	platformcreds "github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/mgmtlink"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/siderolabs/siderolink/pkg/wgtunnel/wgbind"
	"github.com/siderolabs/siderolink/pkg/wgtunnel/wggrpc"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"go.uber.org/zap"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/siderolabs/siderolink/api/siderolink"
)

// hexKey renders a key the way wireguard-go IPC expects (hex).
func hexKey(k wgtypes.Key) string { return hex.EncodeToString(k[:]) }

const (
	testBindingToken = "bind-machine-1"
	testGlobalToken  = "global-link-token"
	testApidPort     = 1024
)

// harness is the full pull-model stack: platform store + management link
// server + converge engine, plus a stock-shaped node whose Talos API is a
// fake maintenance apid capturing ApplyConfiguration calls.
type harness struct {
	store  *state.Store
	server *mgmtlink.Server
	lis    net.Listener
	engine *Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	store, err := state.Open(filepath.Join(t.TempDir(), "converge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	seedTenant(t, store)
	seedMachine(t, store)

	srv, err := mgmtlink.NewServer(mgmtlink.Config{
		Prefix:     netip.MustParsePrefix("fd8c:4216:9a10::/48"),
		JoinToken:  testGlobalToken,
		Listen:     "127.0.0.1:0",
		WGListen:   "",
		StreamPort: 51820,
		MTU:        1280,
	}, store.DB())
	if err != nil {
		t.Fatalf("mgmtlink server: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Listen(ctx, lis) }()

	engine := New(store, srv, nil)
	go engine.Run(ctx)
	t.Cleanup(func() { cancel(); srv.Wait() })

	return &harness{store: store, server: srv, lis: lis, engine: engine}
}

// node is a stock-shaped node: upstream provision client + tunnel stack, with
// a fake maintenance apid serving from its netstack.
type node struct {
	mu      sync.Mutex
	applies []string

	wg *wgdevice.Device
}

// startNode provisions as talos's controller does (binding token in the join
// token slot) and brings up the tunnel plus the fake apid.
func startNode(t *testing.T, h *harness, joinToken string) (*node, error) {
	t.Helper()

	n := &node{}
	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("node key: %v", err)
	}

	gconn, err := grpc.NewClient(h.lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = gconn.Close() })

	req := &pb.ProvisionRequest{NodeUuid: "test-node-uuid", NodePublicKey: privKey.PublicKey().String()}
	if joinToken != "" {
		req.JoinToken = &joinToken
	}
	tunnel := true
	req.WireguardOverGrpc = &tunnel
	resp, err := pb.NewProvisionServiceClient(gconn).Provision(context.Background(), req)
	if err != nil {
		return nil, err
	}

	nodeAddr := netip.MustParsePrefix(resp.NodeAddressPrefix).Addr()

	// One netstack owns the node's IP side; the apid serves from it.
	nodeTun, nodeNet, err := netstack.CreateNetTUN([]netip.Addr{nodeAddr}, nil, 1280)
	if err != nil {
		t.Fatalf("node netstack: %v", err)
	}

	// Fake maintenance apid: TLS with a self-signed cert (real maintenance
	// behavior), capturing ApplyConfiguration payloads.
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{selfSignedCert(t)},
		MinVersion:   tls.VersionTLS12,
	})))
	machineapi.RegisterMachineServiceServer(gs, &fakeApid{n: n})
	tcpLis, err := nodeNet.ListenTCP(&net.TCPAddr{Port: testApidPort})
	if err != nil {
		t.Fatalf("node apid listen: %v", err)
	}
	go func() { _ = gs.Serve(tcpLis) }()

	qp := wgbind.NewQueuePair(128, 128)
	n.wg = wgdevice.NewDevice(nodeTun, wgbind.NewClientBind(qp, zap.NewNop()), wgdevice.NewLogger(wgdevice.LogLevelError, "node-wg: "))

	serverKey, err := wgtypes.ParseKey(resp.ServerPublicKey)
	if err != nil {
		t.Fatalf("parse server key: %v", err)
	}
	// The endpoint is unused by the gRPC transport (packets ride the
	// stream), but the device refuses to initiate without one.
	// wireguard-go IPC uses hex-encoded keys.
	ipc := "private_key=" + hexKey(privKey) +
		"\npublic_key=" + hexKey(serverKey) +
		"\nendpoint=127.0.0.1:51822" +
		"\nallowed_ip=" + resp.ServerAddress + "/128" +
		"\npersistent_keepalive_interval=1\n"
	if err := n.wg.IpcSet(ipc); err != nil {
		t.Fatalf("node ipc set: %v", err)
	}

	streamAddr, err := netip.ParseAddrPort(resp.GrpcPeerAddrPort)
	if err != nil {
		t.Fatalf("parse stream addr: %v", err)
	}
	nodeCtx, nodeCancel := context.WithCancel(context.Background())
	relay := wggrpc.NewRelay(gconn, 5*time.Second, qp, streamAddr)
	go func() { _ = relay.Run(nodeCtx, zap.NewNop()) }()
	t.Cleanup(func() {
		nodeCancel()
		n.wg.Close()
		gs.Stop()
	})
	return n, nil
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node-apid"},
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func seedTenant(t *testing.T, store *state.Store) {
	t.Helper()
	if _, err := store.CreateResource("tenant", "prod", state.TenantSpec{
		KubernetesVersion:    "1.35.0",
		TalosVersion:         "1.12.0",
		ControlPlaneEndpoint: "https://192.168.1.10:6443",
	}, nil, nil, nil); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	bundle, err := platformcreds.GenerateSecretsBundle("1.12.0")
	if err != nil {
		t.Fatalf("gen bundle: %v", err)
	}
	bundleJSON, _ := platformcreds.SecretsBundleJSON(bundle)
	if err := store.SaveTenantSecrets("prod", bundleJSON); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
}

func seedMachine(t *testing.T, store *state.Store) {
	t.Helper()
	if _, err := store.CreateMachine("machine-1", state.MachineSpec{BindingToken: testBindingToken},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil); err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, err := store.UpdateMachineStatus("machine-1", state.MachineStatus{
		Role: "controlplane", Stage: state.StageConfiguring,
	}); err != nil {
		t.Fatalf("machine status: %v", err)
	}
}

// fakeApid is the node's maintenance API: records ApplyConfiguration.
type fakeApid struct {
	machineapi.UnimplementedMachineServiceServer
	n *node
}

func (f *fakeApid) ApplyConfiguration(_ context.Context, req *machineapi.ApplyConfigurationRequest) (*machineapi.ApplyConfigurationResponse, error) {
	f.n.mu.Lock()
	f.n.applies = append(f.n.applies, string(req.Data))
	f.n.mu.Unlock()
	return &machineapi.ApplyConfigurationResponse{}, nil
}

func (n *node) applyCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.applies)
}

func (n *node) lastApply() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.applies) == 0 {
		return ""
	}
	return n.applies[len(n.applies)-1]
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestConfigPull_EndToEnd(t *testing.T) {
	// The full pull-model flow: a bootstrapping node joins the management
	// link, the engine resolves it via its binding token, renders the
	// machine's config, and delivers it over the tunnel to the node's
	// maintenance Talos API.
	h := newHarness(t)
	n, err := startNode(t, h, testBindingToken)
	if err != nil {
		t.Fatalf("start node: %v", err)
	}

	// Late redial through the engine: once the WG session is up, delivery
	// should succeed where the provision-time attempt raced it.
	time.Sleep(2 * time.Second)
	if err := h.engine.ConvergeMachine(context.Background(), "machine-1"); err != nil {
		t.Fatalf("late ConvergeMachine: %v", err)
	}

	waitFor(t, 30*time.Second, func() bool { return n.applyCount() > 0 },
		"timed out waiting for ApplyConfiguration to reach the node")

	got := n.lastApply()
	if !strings.Contains(got, "controlPlane") && !strings.Contains(got, "controlplane") {
		t.Errorf("delivered config is not a controlplane config (first 200 chars: %.200q)", got)
	}

	// The machine reflects the convergence.
	m, err := h.store.GetMachine("machine-1")
	if err != nil {
		t.Fatalf("get machine: %v", err)
	}
	if !m.Status.ConfigCurrent {
		t.Error("ConfigCurrent = false, want true")
	}
	if m.Status.Stage != state.StageReady {
		t.Errorf("Stage = %q, want %q", m.Status.Stage, state.StageReady)
	}
	if !m.Spec.Connected {
		t.Error("Connected = false, want true")
	}
}

func TestConfigPull_RejectsUnknownBindingToken(t *testing.T) {
	// A node presenting a token no machine record declares must not
	// provision — nothing to converge to. The global link token still
	// authorizes enrollment (enrollment ≠ machine mapping).
	h := newHarness(t)
	if _, err := startNode(t, h, "unknown-token"); err == nil {
		t.Error("provision succeeded for unknown binding token, want rejection")
	}
}

func TestConfigPull_ConvergeOnChange(t *testing.T) {
	// After the initial delivery, a change-driven re-converge (the entry
	// point the config API calls on patch/tenant changes) re-delivers.
	h := newHarness(t)
	n, err := startNode(t, h, testBindingToken)
	if err != nil {
		t.Fatalf("start node: %v", err)
	}

	waitFor(t, 60*time.Second, func() bool { return n.applyCount() >= 1 },
		"timed out waiting for initial ApplyConfiguration")

	if err := h.engine.ConvergeMachine(context.Background(), "machine-1"); err != nil {
		t.Fatalf("ConvergeMachine: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool { return n.applyCount() >= 2 },
		"timed out waiting for change-driven ApplyConfiguration")

	if err := h.engine.ConvergeMachine(context.Background(), "machine-not-connected"); err == nil {
		t.Error("ConvergeMachine succeeded for an unconnected machine, want error")
	}
}
