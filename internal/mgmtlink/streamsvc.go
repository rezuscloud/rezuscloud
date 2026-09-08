package mgmtlink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/siderolabs/siderolink/api/siderolink"
)

// PeerAddrKey is the gRPC metadata key a stock Talos node sets to its virtual
// address:port (matching upstream wggrpc.PeerAddrKey).
const PeerAddrKey = "x-siderolink-ipv6-addr"

var (
	errPeerNotAllowed = status.Error(codes.PermissionDenied, "peer not allowed")
	errPeerReplaced   = status.Error(codes.Aborted, "peer replaced")
)

// streamService implements sidero.link.WireGuardOverGRPCService: each node
// opens one bidirectional stream and exchanges raw WireGuard packets over it.
// Inbound packets are handed to the WG device via the bind's inbound queue;
// outbound packets flow from the device into the node's per-peer send queue.
type streamService struct {
	pb.UnimplementedWireGuardOverGRPCServiceServer

	provisions *provisionService
	bind       *serverBind

	mu      sync.Mutex
	streams map[string]*streamHandle
}

type streamHandle struct {
	cancel context.CancelCauseFunc
}

func newStreamService(provisions *provisionService, bind *serverBind) *streamService {
	return &streamService{
		provisions: provisions,
		bind:       bind,
		streams:    map[string]*streamHandle{},
	}
}

// CreateStream implements pb.WireGuardOverGRPCServiceServer.
func (s *streamService) CreateStream(srv pb.WireGuardOverGRPCService_CreateStreamServer) error {
	peerAddr, err := peerAddrFromContext(srv.Context())
	if err != nil {
		return err
	}
	addrPort, err := netip.ParseAddrPort(peerAddr)
	if err != nil {
		return fmt.Errorf("invalid %s header %q: %w", PeerAddrKey, peerAddr, err)
	}
	if !s.provisions.allowStream(peerAddr) {
		slog.Warn("mgmtlink stream from unallocated address", "addr", peerAddr)
		return errPeerNotAllowed
	}
	s.provisions.noteStream(peerAddr)
	sendQueue, ok := s.bind.getSendQueue(peerAddr, true)
	if !ok {
		return status.Error(codes.Unavailable, "server is shutting down")
	}

	// A replacing stream (node reconnect) takes over the peer slot; the old
	// stream must exit without tearing the slot down for its replacement.
	s.mu.Lock()
	if existing, ok := s.streams[peerAddr]; ok {
		existing.cancel(errPeerReplaced)
	}
	ctx, cancel := context.WithCancelCause(srv.Context())
	handle := &streamHandle{cancel: cancel}
	s.streams[peerAddr] = handle
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		owns := s.streams[peerAddr] == handle
		if owns {
			delete(s.streams, peerAddr)
		}
		s.mu.Unlock()
		if owns {
			s.bind.dropPeer(peerAddr)
		}
	}()

	eg, ctx := errgroup.WithContext(ctx)

	// Inbound: node → WG device.
	eg.Go(func() error {
		for {
			pkt, err := srv.Recv()
			if err != nil {
				return s.causeOr(ctx, err)
			}
			if err := s.bind.enqueueInbound(ctx, addrPort, pkt.Data); err != nil {
				return s.causeOr(ctx, err)
			}
		}
	})

	// Outbound: WG device → node. Selecting on ctx (not a bare Pop) so a
	// replaced stream's goroutine exits instead of stealing its successor's
	// packets from the shared queue.
	eg.Go(func() error {
		for {
			var data []byte
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case d := <-sendQueue.ch:
				data = d
			}
			if err := srv.Send(&pb.PeerPacket{Data: data}); err != nil {
				return s.causeOr(ctx, err)
			}
		}
	})

	err = eg.Wait()
	if errors.Is(context.Cause(ctx), errPeerReplaced) {
		return errPeerReplaced
	}
	return err
}

func (s *streamService) causeOr(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
		return cause
	}
	return err
}

// activeStreams reports currently attached tunnel-mode node addresses.
func (s *streamService) activeStreams() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.streams))
	for addr := range s.streams {
		out = append(out, addr)
	}
	return out
}

func peerAddrFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing gRPC metadata")
	}
	vals := md.Get(PeerAddrKey)
	if len(vals) == 0 || vals[0] == "" {
		return "", fmt.Errorf("missing %s metadata", PeerAddrKey)
	}
	return vals[0], nil
}

// compile-time interface check.
var _ pb.WireGuardOverGRPCServiceServer = (*streamService)(nil)
