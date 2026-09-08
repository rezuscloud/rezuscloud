package mgmtlink

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/siderolabs/siderolink/pkg/wgtunnel/wgbind"
	"github.com/siderolabs/siderolink/pkg/wgtunnel/wggrpc"
	"go.uber.org/zap"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"

	pb "github.com/siderolabs/siderolink/api/siderolink"
)

// testServer is a running Server with its listener.
type testServer struct {
	*Server
	lis net.Listener
}

func newTestServer(t *testing.T, mutate func(*Config)) *testServer {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mgmtlink.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := Config{
		Prefix:     netip.MustParsePrefix("fd8c:4216:9a10::/48"),
		JoinToken:  "test-join-token",
		Listen:     "127.0.0.1:0",
		WGListen:   "", // tunnel-only: exercises the gRPC transport
		StreamPort: 51820,
		MTU:        1280,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	srv, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Listen(ctx, lis) }()
	t.Cleanup(func() {
		cancel()
		srv.Wait()
	})
	return &testServer{Server: srv, lis: lis}
}

// testNode is a stock-Talos-shaped node stack: the upstream client bind and
// relay, a wireguard-go device over netstack (standing in for the kernel
// userspace tunnel), and a provision client.
type testNode struct {
	privKey    wgtypes.Key
	prefix     netip.Prefix
	streamAddr netip.AddrPort
	wg         *wgdevice.Device
	relay      *wggrpc.Relay
	relayErr   chan error
	cancel     context.CancelFunc
}

// provision performs the Provision RPC exactly as talos's controller does.
func provisionNode(t *testing.T, srv *testServer, nodeUUID string, key wgtypes.Key, tunnel bool, joinToken string) *pb.ProvisionResponse {
	t.Helper()
	gconn, gerr := grpc.NewClient(srv.lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if gerr != nil {
		t.Fatalf("grpc dial: %v", gerr)
	}
	t.Cleanup(func() { _ = gconn.Close() })

	req := &pb.ProvisionRequest{
		NodeUuid:      nodeUUID,
		NodePublicKey: key.PublicKey().String(),
	}
	if joinToken != "" {
		req.JoinToken = &joinToken
	}
	if tunnel {
		t := true
		req.WireguardOverGrpc = &t
	}
	resp, perr := pb.NewProvisionServiceClient(gconn).Provision(context.Background(), req)
	if perr != nil {
		t.Fatalf("Provision: %v", perr)
	}
	return resp
}

// start brings the node's tunnel up from a provision response, mirroring
// talos's UserspaceWireguardController: upstream ClientBind + queue pair +
// relay to the server, WG device over netstack.
func (n *testNode) start(t *testing.T, srv *testServer, resp *pb.ProvisionResponse) {
	t.Helper()

	n.prefix = netip.MustParsePrefix(resp.NodeAddressPrefix)
	nodeAddr := n.prefix.Addr()

	// One netstack owns the node's IP side: the WG device decrypts into it,
	// and the echo listener (the node's fake Talos API) serves from it.
	nodeTun, nodeNet, err := netstack.CreateNetTUN([]netip.Addr{nodeAddr}, nil, 1280)
	if err != nil {
		t.Fatalf("node netstack: %v", err)
	}
	startEchoOnNet(t, nodeNet, 50000)

	qp := wgbind.NewQueuePair(128, 128)
	n.wg = wgdevice.NewDevice(nodeTun, wgbind.NewClientBind(qp, zap.NewNop()), wgdevice.NewLogger(wgdevice.LogLevelError, "node-wg: "))

	serverKey, err := wgtypes.ParseKey(resp.ServerPublicKey)
	if err != nil {
		t.Fatalf("parse server key: %v", err)
	}
	// The endpoint value is unused by the gRPC transport (packets ride the
	// stream), but real Talos always sets it from the resolved WG endpoint,
	// and the device refuses to initiate without one.
	cfg := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=127.0.0.1:51822\nallowed_ip=%s/128\npersistent_keepalive_interval=1\n",
		hex.EncodeToString(n.privKey[:]), hex.EncodeToString(serverKey[:]), resp.ServerAddress)
	if err := n.wg.IpcSet(cfg); err != nil {
		t.Fatalf("node ipc set: %v", err)
	}

	gconn, err := grpc.NewClient(srv.lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = gconn.Close() })

	streamAddr, err := netip.ParseAddrPort(resp.GrpcPeerAddrPort)
	if err != nil {
		t.Fatalf("parse stream addr: %v", err)
	}
	n.streamAddr = streamAddr

	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	n.relay = wggrpc.NewRelay(gconn, 5*time.Second, qp, streamAddr)
	n.relayErr = make(chan error, 1)
	go func() { n.relayErr <- n.relay.Run(ctx, zap.NewExample()) }()
	t.Cleanup(func() {
		cancel()
		n.wg.Close()
	})
}

// startEchoOnNet runs a TCP echo server inside the node's netstack (the
// node's fake Talos API): the tunnel's far end.
func startEchoOnNet(t *testing.T, ns *netstack.Net, port uint16) {
	t.Helper()
	lis, err := ns.ListenTCP(&net.TCPAddr{Port: int(port)})
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = lis.Close() })
}

func TestInterop_FullTunnelFlow(t *testing.T) {
	srv := newTestServer(t, nil)

	nodeKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	const uuid = "3f0c2f7e-9b1a-4c2d-8e5f-1a2b3c4d5e6f"

	resp := provisionNode(t, srv, uuid, nodeKey, true, "test-join-token")

	// Response contract (what the talos controller applies).
	if resp.ServerPublicKey == "" || resp.ServerAddress == "" {
		t.Fatalf("provision response incomplete: %+v", resp)
	}
	if got := resp.NodeAddressPrefix; got != "fd8c:4216:9a10:1::/64" {
		t.Errorf("NodeAddressPrefix = %q, want the first allocated /64", got)
	}
	if !strings.HasPrefix(resp.GrpcPeerAddrPort, "[fd8c:4216:9a10:1::]:") {
		t.Errorf("GrpcPeerAddrPort = %q, want an addr in the node's /64", resp.GrpcPeerAddrPort)
	}
	if len(resp.ServerEndpoint) == 0 {
		t.Error("provision response must carry at least one WG endpoint (node controller requires it)")
	}

	node := &testNode{privKey: nodeKey}
	node.start(t, srv, resp)

	// Management → node over the tunnel: dial the node's echo ("Talos API")
	// through the server's netstack. This is the whole point of the link.
	var conn net.Conn
	var dialErr error
	for i := 0; i < 30; i++ { // handshake + first keepalive may need a beat
		conn, dialErr = srv.DialContext(context.Background(), "tcp", net.JoinHostPort(node.prefix.Addr().String(), "50000"))
		if dialErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if dialErr != nil {
		t.Fatalf("dial node through tunnel: %v", dialErr)
	}
	defer conn.Close()

	msg := "rezuscloud management link"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil && len(got) == 0 {
		t.Fatalf("echo: %v", err)
	}
	if string(got) != msg {
		t.Errorf("echo = %q, want %q", got, msg)
	}

	// Registry reflects the node.
	peers := srv.Peers()
	if len(peers) != 1 || !peers[0].Connected {
		t.Fatalf("peers = %+v, want one connected peer", peers)
	}
	if peers[0].NodeUUID != uuid {
		t.Errorf("peer uuid = %q, want %q", peers[0].NodeUUID, uuid)
	}
}

func TestInterop_StableAllocation(t *testing.T) {
	srv := newTestServer(t, nil)
	key, _ := wgtypes.GeneratePrivateKey()

	r1 := provisionNode(t, srv, "uuid-1", key, true, "test-join-token")
	r2 := provisionNode(t, srv, "uuid-1", key, true, "test-join-token")
	if r1.NodeAddressPrefix != r2.NodeAddressPrefix || r1.GrpcPeerAddrPort != r2.GrpcPeerAddrPort {
		t.Errorf("re-provision must be stable: %s/%s vs %s/%s",
			r1.NodeAddressPrefix, r1.GrpcPeerAddrPort, r2.NodeAddressPrefix, r2.GrpcPeerAddrPort)
	}

	key2, _ := wgtypes.GeneratePrivateKey()
	r3 := provisionNode(t, srv, "uuid-2", key2, true, "test-join-token")
	if r3.NodeAddressPrefix == r1.NodeAddressPrefix {
		t.Error("distinct nodes must get distinct /64s")
	}
	if r3.NodeAddressPrefix != "fd8c:4216:9a10:2::/64" {
		t.Errorf("second node prefix = %q, want fd8c:4216:9a10:2::/64", r3.NodeAddressPrefix)
	}
}

func TestInterop_JoinTokenRejected(t *testing.T) {
	srv := newTestServer(t, nil)
	key, _ := wgtypes.GeneratePrivateKey()

	conn, err := grpc.NewClient(srv.lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, err = pb.NewProvisionServiceClient(conn).Provision(context.Background(), &pb.ProvisionRequest{
		NodeUuid:      "uuid-x",
		NodePublicKey: key.PublicKey().String(),
		JoinToken:     ptr("wrong-token"),
	})
	if err == nil || status.Code(err) == codes.OK {
		t.Fatalf("wrong join token must be rejected, got %v", err)
	}
}

func TestInterop_StreamFromUnallocatedAddressRejected(t *testing.T) {
	srv := newTestServer(t, nil)

	conn, err := grpc.NewClient(srv.lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(PeerAddrKey, "[fd8c:4216:9a10:99::]:51820"))
	stream, err := pb.NewWireGuardOverGRPCServiceClient(conn).CreateStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("unallocated stream = %v, want PermissionDenied", err)
	}
}

func ptr(s string) *string { return &s }
