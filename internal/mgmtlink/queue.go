package mgmtlink

import "context"

// packetQueue is a backpressured FIFO for packets entering the WG device.
// It is never closed: wireguard-go legitimately Close→Opens its bind
// (BindUpdate on interface state changes), so queue lifetime is independent
// of bind lifetime. Termination is per-Open via context.
type packetQueue struct {
	ch chan inboundPacket
}

func newPacketQueue(cap int) *packetQueue {
	return &packetQueue{ch: make(chan inboundPacket, cap)}
}

// Push blocks under backpressure — the caller's stream context cancels it.
func (q *packetQueue) Push(ctx context.Context, v inboundPacket) error {
	select {
	case q.ch <- v:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// ringQueue is a lossy latest-matters queue for per-peer outbound packets:
// if a node's stream stalls, dropping oldest queued WG packets is correct —
// WireGuard retransmits; its timers live above us. Push never blocks, so a
// slow stream cannot backpressure the WG device. Per-peer lifetime: closed
// only when the peer's stream is torn down.
type ringQueue[T any] struct {
	ch chan T
}

func newRingQueue[T any](cap int) *ringQueue[T] {
	return &ringQueue[T]{ch: make(chan T, cap)}
}

// Push drops the oldest item when full (never blocks).
func (q *ringQueue[T]) Push(v T) {
	select {
	case q.ch <- v:
	default:
		select {
		case <-q.ch:
		default:
		}
		select {
		case q.ch <- v:
		default:
		}
	}
}

// Close unblocks Poppers (peer stream teardown).
func (q *ringQueue[T]) Close() {
	close(q.ch)
}
