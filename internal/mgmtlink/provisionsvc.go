package mgmtlink

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	pb "github.com/siderolabs/siderolink/api/siderolink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// provisionService implements sidero.link.ProvisionService — the one RPC a
// stock Talos node calls before bringing up its tunnel: join-token check,
// stable /64 allocation, and the response fields the node controller applies
// (verified against talos's siderolink.ManagerController).
type provisionService struct {
	pb.UnimplementedProvisionServiceServer

	cfg       Config
	store     *peerStore
	device    deviceMutator
	serverKey wgtypes.Key
	serverAP  netip.Addr // server's own tunnel address

	mu           sync.Mutex
	streamTokens map[string]string // virtual addr:port → node identity
	events       *peerEvents
	// acceptToken authorizes a presented join token. When nil, only the
	// configured global link token is accepted. A platform wiring machine
	// binding tokens extends this (the presented token is passed verbatim
	// in the peer event so the platform can resolve it). Guarded by mu.
	acceptToken func(token string) bool
}

// deviceMutator is the slice of the wireguard-go device the provisioner
// drives (an interface for tests).
type deviceMutator interface {
	// ApplyPeer registers/updates a peer: pubkey + allowed prefix.
	ApplyPeer(pubKey wgtypes.Key, allowed netip.Prefix) error
}

func newProvisionService(cfg Config, store *peerStore, device deviceMutator, serverKey wgtypes.Key, serverAP netip.Addr, events *peerEvents) *provisionService {
	return &provisionService{
		cfg:          cfg,
		store:        store,
		device:       device,
		serverKey:    serverKey,
		serverAP:     serverAP,
		streamTokens: map[string]string{},
		events:       events,
	}
}

// Provision implements pb.ProvisionServiceServer.
func (s *provisionService) Provision(_ context.Context, req *pb.ProvisionRequest) (*pb.ProvisionResponse, error) {
	accepted := constantTimeToken(req.GetJoinToken(), s.cfg.JoinToken)
	if !accepted {
		s.mu.Lock()
		accept := s.acceptToken
		s.mu.Unlock()
		if accept != nil {
			accepted = accept(req.GetJoinToken())
		}
	}
	if !accepted {
		return nil, fmt.Errorf("invalid join token")
	}
	pubKey, err := wgtypes.ParseKey(req.NodePublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid node public key: %w", err)
	}

	identity := identityFor(req.NodeUuid, req.GetNodeUniqueToken())
	peer, created, err := s.store.findOrAllocate(identity, req.NodeUuid, req.NodePublicKey, req.GetTalosVersion(), s.cfg.Prefix, s.cfg.StreamPort)
	if err != nil {
		return nil, fmt.Errorf("allocate node: %w", err)
	}

	prefix, err := netip.ParsePrefix(peer.Prefix)
	if err != nil {
		return nil, fmt.Errorf("stored prefix %q: %w", peer.Prefix, err)
	}

	// Register the peer on the WG device (idempotent across re-provisions
	// and key rotations).
	if err := s.device.ApplyPeer(pubKey, prefix); err != nil {
		return nil, fmt.Errorf("register peer: %w", err)
	}

	// Tunnel mode: hand the node its virtual stream address. Kernel mode
	// leaves it empty — the node routes over UDP instead.
	resp := &pb.ProvisionResponse{
		ServerEndpoint:    s.cfg.AdvertisedEndpoints,
		ServerPublicKey:   s.serverKey.PublicKey().String(),
		NodeAddressPrefix: peer.Prefix,
		ServerAddress:     s.serverAP.String(),
	}
	s.events.emit(PeerEvent{
		Kind:         PeerConnected,
		NodeUUID:     peer.NodeUUID,
		JoinToken:    req.GetJoinToken(),
		NodeAddrPort: peer.StreamAddr,
	})
	if req.GetWireguardOverGrpc() {
		s.mu.Lock()
		s.streamTokens[peer.StreamAddr] = peer.Identity
		s.mu.Unlock()
		resp.GrpcPeerAddrPort = peer.StreamAddr
	}

	slog.Info("mgmtlink provision",
		"identity", identity,
		"node_uuid", peer.NodeUUID,
		"prefix", peer.Prefix,
		"tunnel", req.GetWireguardOverGrpc(),
		"created", created,
	)
	return resp, nil
}

// allowStream checks a CreateStream's virtual address against allocated
// tunnel-mode nodes (the upstream AllowedPeers semantic).
func (s *provisionService) allowStream(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.streamTokens[addr]
	return ok
}

// noteStream marks the node as seen when a stream attaches.
func (s *provisionService) noteStream(addr string) {
	s.mu.Lock()
	identity, ok := s.streamTokens[addr]
	s.mu.Unlock()
	if ok {
		s.store.touch(identity)
	}
}

// constantTimeToken reports a == b without timing leaks.
func constantTimeToken(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// compile-time interface check.
var _ pb.ProvisionServiceServer = (*provisionService)(nil)
