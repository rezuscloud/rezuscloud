package mgmtlink

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// serverBind is a wireguard-go conn.Bind that serves kernel-mode nodes over a
// plain UDP socket (the default bind) and tunnel-mode nodes over gRPC streams
// (the per-peer send queues fed by streamService). Routing is by endpoint
// identity: only endpoints this bind produced from gRPC traffic route to
// queues; everything else goes out over UDP.
//
// Lifecycle: wireguard-go Close→Opens the bind when the device state changes
// (BindUpdate), so Close must not be treated as terminal — per-Open state
// lives in openCtx; the shared queues outlive bind cycles.
type serverBind struct {
	defaultConn conn.Bind // nil → gRPC-only (tests, UDP-less deployments)

	inbound *packetQueue

	mu        sync.RWMutex
	peers     map[string]*ringQueue[[]byte] // key: node addr:port as sent on the stream
	openCtx   context.Context
	openStop  context.CancelFunc
	udpFns    []conn.ReceiveFunc
	udpClosed bool
}

// inboundPacket is one packet pulled off a node's gRPC stream, waiting for
// the WG device's receive func.
type inboundPacket struct {
	data []byte
	ap   netip.AddrPort
}

func newServerBind(defaultConn conn.Bind) *serverBind {
	return &serverBind{
		defaultConn: defaultConn,
		inbound:     newPacketQueue(1024),
		peers:       map[string]*ringQueue[[]byte]{},
	}
}

// Open implements conn.Bind.
func (b *serverBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	var (
		fns        []conn.ReceiveFunc
		actualPort uint16
		err        error
	)
	if b.defaultConn != nil {
		fns, actualPort, err = b.defaultConn.Open(port)
		if err != nil {
			return nil, 0, err
		}
		b.udpFns = fns
		b.udpClosed = false
	}

	b.mu.Lock()
	b.openCtx, b.openStop = context.WithCancel(context.Background())
	openCtx := b.openCtx
	b.mu.Unlock()

	return append(fns, func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		return b.receiveGRPC(openCtx, packets, sizes, eps)
	}), actualPort, nil
}

// receiveGRPC is the receive func serving gRPC-transported packets: one
// packet per call, endpoint tagged with the node's virtual address.
func (b *serverBind) receiveGRPC(openCtx context.Context, packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	select {
	case pkt := <-b.inbound.ch:
		n := copy(packets[0], pkt.data)
		sizes[0] = n
		eps[0] = grpcEndpoint{ap: pkt.ap}
		return 1, nil
	case <-openCtx.Done():
		return 0, net.ErrClosed
	}
}

// Send implements conn.Bind.
func (b *serverBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	ge, ok := ep.(grpcEndpoint)
	if !ok {
		if b.defaultConn == nil {
			return nil // no UDP transport configured; unknown endpoint — drop
		}
		return b.defaultConn.Send(bufs, ep)
	}

	q, ok := b.getSendQueue(ge.ap.String(), false)
	if !ok {
		return nil // node's stream is down; WG retry timers handle it
	}
	for _, buf := range bufs {
		cp := make([]byte, len(buf))
		copy(cp, buf)
		q.Push(cp)
	}
	return nil
}

// ParseEndpoint implements conn.Bind — only kernel-mode peers are parsed
// from strings (real UDP endpoints). gRPC endpoints are created exclusively
// by receiveGRPC.
func (b *serverBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	if b.defaultConn == nil {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			return nil, err
		}
		return udpEndpoint{ap: ap}, nil
	}
	return b.defaultConn.ParseEndpoint(s)
}

func (b *serverBind) SetMark(mark uint32) error {
	if b.defaultConn == nil {
		return nil
	}
	return b.defaultConn.SetMark(mark)
}

func (b *serverBind) BatchSize() int {
	if b.defaultConn == nil {
		return 1
	}
	return b.defaultConn.BatchSize()
}

// Close implements conn.Bind. Called by the device on BindUpdate as well as
// shutdown — it must only stop this Open cycle's receive funcs.
func (b *serverBind) Close() error {
	b.mu.Lock()
	if b.openStop != nil {
		b.openStop()
		b.openStop = nil
	}
	udpClosed := b.udpClosed
	if !udpClosed && b.defaultConn != nil {
		b.udpClosed = true
	}
	b.mu.Unlock()

	if !udpClosed && b.defaultConn != nil {
		return b.defaultConn.Close()
	}
	return nil
}

// getSendQueue returns (creating if needed) the outbound queue for a node's
// virtual address. The streamService pops from it.
func (b *serverBind) getSendQueue(addr string, create bool) (*ringQueue[[]byte], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if q, ok := b.peers[addr]; ok {
		return q, true
	}
	if !create {
		return nil, false
	}
	q := newRingQueue[[]byte](128)
	b.peers[addr] = q
	return q, true
}

// dropPeer removes a node's outbound queue (stream gone or peer replaced).
func (b *serverBind) dropPeer(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q, ok := b.peers[addr]; ok {
		q.Close()
		delete(b.peers, addr)
	}
}

// enqueueInbound accepts a packet from a node's gRPC stream. It blocks under
// backpressure — the stream handler's context cancels it on teardown.
func (b *serverBind) enqueueInbound(ctx context.Context, ap netip.AddrPort, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	return b.inbound.Push(ctx, inboundPacket{data: cp, ap: ap})
}

// grpcEndpoint is the conn.Endpoint of a tunnel-mode node: its virtual
// address:port as declared at provisioning.
type grpcEndpoint struct {
	ap netip.AddrPort
}

func (e grpcEndpoint) ClearSrc()           {}
func (e grpcEndpoint) SrcToString() string { return "" }
func (e grpcEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }
func (e grpcEndpoint) DstToString() string { return e.ap.String() }
func (e grpcEndpoint) DstIP() netip.Addr   { return e.ap.Addr() }
func (e grpcEndpoint) DstToBytes() []byte {
	b := e.ap.Addr().As16()
	return b[:]
}

// udpEndpoint mirrors UDP endpoints when no default UDP bind exists (tests).
type udpEndpoint struct {
	ap netip.AddrPort
}

func (e udpEndpoint) ClearSrc()           {}
func (e udpEndpoint) SrcToString() string { return "" }
func (e udpEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }
func (e udpEndpoint) DstToString() string { return e.ap.String() }
func (e udpEndpoint) DstIP() netip.Addr   { return e.ap.Addr() }
func (e udpEndpoint) DstToBytes() []byte {
	b := e.ap.Addr().As16()
	return b[:]
}
