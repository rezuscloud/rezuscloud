package mgmtlink

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"

	pb "github.com/siderolabs/siderolink/api/siderolink"
)

// Server is the embedded management-link server. It terminates both node
// transports (gRPC tunnel + UDP), runs the WG device over an in-process
// netstack (no host privileges, no kernel interfaces), and exposes
// DialContext for management→node connections (status probes, `talosctl`
// reachability, later config-pull callbacks).
// PeerEvent notifies the platform of tunnel lifecycle: a node provisioned
// (connected) or its stream went away (disconnected). JoinToken is the token
// the node presented — the platform's binding-token → machine key.
type PeerEvent struct {
	Kind PeerEventKind

	NodeUUID     string
	JoinToken    string
	NodeAddrPort string // virtual stream address (tunnel mode)
}

// PeerEventKind discriminates PeerEvent.
type PeerEventKind string

const (
	// PeerConnected — the node provisioned successfully and may attach its
	// stream.
	PeerConnected PeerEventKind = "connected"
	// PeerDisconnected — the node's stream detached.
	PeerDisconnected PeerEventKind = "disconnected"
)

// peerEvents fans peer lifecycle out to the platform (single listener — the
// converge engine; ordering matters, so no fan-out to many).
type peerEvents struct {
	mu sync.Mutex
	fn func(PeerEvent)
}

// emit invokes the listener on its own goroutine: listeners (the converge
// engine) drive delivery, which must never block the provision RPC — the
// node's controller can't finish provisioning until the response returns.
func (e *peerEvents) emit(ev PeerEvent) {
	e.mu.Lock()
	fn := e.fn
	e.mu.Unlock()
	if fn != nil {
		go fn(ev)
	}
}

type Server struct {
	events  *peerEvents
	cfg     Config
	store   *peerStore
	bind    *serverBind
	device  *wgdevice.Device
	net     *netstack.Net
	grpc    *grpc.Server
	prov    *provisionService
	streams *streamService
	cancel  context.CancelFunc
	done    chan struct{}
}

// SetPeerListener registers the platform's peer-lifecycle listener (connected
// / disconnected). Call before Listen.
func (s *Server) SetPeerListener(fn func(PeerEvent)) {
	s.events.mu.Lock()
	s.events.fn = fn
	s.events.mu.Unlock()
}

// SetTokenAcceptor extends join-token authorization beyond the configured
// global link token — the platform accepts its machines' binding tokens
// here. The accepted token is passed verbatim in PeerEvent.JoinToken.
// Call before Listen.
func (s *Server) SetTokenAcceptor(fn func(token string) bool) {
	s.prov.mu.Lock()
	s.prov.acceptToken = fn
	s.prov.mu.Unlock()
}

// NewServer assembles the server. It does not listen — call Listen.
func NewServer(cfg Config, db *sql.DB) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, fmt.Errorf("mgmtlink is not configured")
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1280
	}

	store, err := newPeerStore(db)
	if err != nil {
		return nil, err
	}

	// The node controller requires at least one WG endpoint in provision
	// responses even in tunnel-only deployments (it builds the link spec
	// from it). Default to the WG listener, else the gRPC listen address.
	if len(cfg.AdvertisedEndpoints) == 0 {
		if cfg.WGListen != "" {
			cfg.AdvertisedEndpoints = []string{cfg.WGListen}
		} else {
			cfg.AdvertisedEndpoints = []string{cfg.Listen}
		}
	}

	// Server WG key: generated once, persisted — node-visible identity must
	// survive restarts or every node re-provisions.
	serverKey, err := loadOrCreateServerKey(store)
	if err != nil {
		return nil, err
	}
	serverAP := prefixNthAddr(cfg.Prefix, 0, 1) // <prefix>:0::1

	// In-process IP stack: the WG device's plaintext side. Management dials
	// node ULAs through nsNet below — no TUN device, no privileges.
	nsTun, nsNet, err := netstack.CreateNetTUN([]netip.Addr{serverAP}, nil, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("create netstack: %w", err)
	}
	_ = nsTun // device owns (and closes) the tun side

	var udpBind conn.Bind
	if cfg.WGListen != "" {
		udpBind = conn.NewDefaultBind()
	}
	bind := newServerBind(udpBind)

	device := wgdevice.NewDevice(nsTun, bind, wgdevice.NewLogger(wgdevice.LogLevelError, "mgmtlink-wg: "))

	if err := device.IpcSet(fmt.Sprintf("private_key=%s\n", hexKey(serverKey))); err != nil {
		device.Close()
		return nil, fmt.Errorf("apply wg private key: %w", err)
	}

	// Recover peers persisted by previous runs.
	peers, err := store.list()
	if err != nil {
		device.Close()
		return nil, err
	}
	for _, p := range peers {
		key, err := wgtypes.ParseKey(p.PubKey)
		if err != nil {
			slog.Warn("mgmtlink skipping peer with unparsable key", "identity", p.Identity)
			continue
		}
		prefix, err := netip.ParsePrefix(p.Prefix)
		if err != nil {
			continue
		}
		if err := applyPeer(device, key, prefix); err != nil {
			device.Close()
			return nil, fmt.Errorf("restore peer %s: %w", p.Identity, err)
		}
	}

	events := &peerEvents{}
	prov := newProvisionService(cfg, store, &wgDeviceMutator{device}, serverKey, serverAP, events)
	streams := newStreamService(prov, bind, events)

	gsrv := grpc.NewServer()
	pb.RegisterProvisionServiceServer(gsrv, prov)
	pb.RegisterWireGuardOverGRPCServiceServer(gsrv, streams)

	return &Server{
		events:  events,
		cfg:     cfg,
		store:   store,
		bind:    bind,
		device:  device,
		net:     nsNet,
		grpc:    gsrv,
		prov:    prov,
		streams: streams,
		done:    make(chan struct{}),
	}, nil
}

// Listen serves the gRPC endpoint (blocking). Run it in a goroutine.
func (s *Server) Listen(ctx context.Context, lis net.Listener) error {
	ctx, s.cancel = context.WithCancel(ctx)
	defer close(s.done)

	errCh := make(chan error, 1)
	go func() {
		s.grpc.Serve(lis)
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		s.grpc.Stop()
		<-errCh
	case err := <-errCh:
		return err
	}

	s.device.Close()
	return nil
}

// DialContext connects from the management plane to a node over the tunnel
// (e.g. "tcp", "[fd8c:4216:9a10:1::]:50000" for the node's Talos API).
func (s *Server) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return s.net.DialContext(ctx, network, address)
}

// Peers snapshots the node registry with live state.
func (s *Server) Peers() []PeerStatus {
	peers, err := s.store.list()
	if err != nil {
		slog.Warn("mgmtlink peer list failed", "err", err)
		return nil
	}
	live := map[string]bool{}
	for _, addr := range s.streams.activeStreams() {
		live[addr] = true
	}
	out := make([]PeerStatus, 0, len(peers))
	for _, p := range peers {
		out = append(out, PeerStatus{
			Identity:   p.Identity,
			NodeUUID:   p.NodeUUID,
			Prefix:     p.Prefix,
			StreamAddr: p.StreamAddr,
			Connected:  live[p.StreamAddr],
			LastSeen:   p.LastSeen,
		})
	}
	return out
}

// PeerStatus is one node's management-link state.
type PeerStatus struct {
	Identity   string
	NodeUUID   string
	Prefix     string
	StreamAddr string
	Connected  bool
	LastSeen   time.Time
}

// Wait blocks until the server has shut down.
func (s *Server) Wait() { <-s.done }

// wgDeviceMutator adapts the wireguard-go device to deviceMutator via the
// IPC protocol (the same wire format wgctrl speaks).
type wgDeviceMutator struct{ device *wgdevice.Device }

func (m *wgDeviceMutator) ApplyPeer(pubKey wgtypes.Key, allowed netip.Prefix) error {
	return applyPeer(m.device, pubKey, allowed)
}

func applyPeer(device *wgdevice.Device, pubKey wgtypes.Key, allowed netip.Prefix) error {
	return device.IpcSet(fmt.Sprintf("public_key=%s\nallowed_ip=%s\n", hexKey(pubKey), allowed.String()))
}

// hexKey encodes a key the way the wireguard-go IPC protocol expects (hex).
func hexKey(k wgtypes.Key) string {
	return hex.EncodeToString(k[:])
}

func loadOrCreateServerKey(store *peerStore) (wgtypes.Key, error) {
	v, err := store.setting("server_key")
	if err != nil {
		return wgtypes.Key{}, err
	}
	if v != "" {
		key, err := wgtypes.ParseKey(v)
		if err == nil {
			return key, nil
		}
		slog.Warn("mgmtlink stored server key unparsable, regenerating")
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, err
	}
	if err := store.setSetting("server_key", key.String()); err != nil {
		return wgtypes.Key{}, err
	}
	return key, nil
}

// prefixNthAddr returns the n-th /64 inside prefix, address suffix `last`.
func prefixNthAddr(prefix netip.Prefix, n uint16, last byte) netip.Addr {
	b := prefix.Masked().Addr().As16()
	b[6] = byte(n >> 8)
	b[7] = byte(n)
	b[15] = last
	addr, ok := netip.AddrFromSlice(b[:])
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}
