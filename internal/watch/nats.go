package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// subjectPrefix namespaces all NATS subjects so the embedded server's subjects
// don't collide with any future external NATS usage.
const subjectPrefix = "rezuscloud.events."

// NATSBus is a NATS-backed implementation of Bus. It uses an embedded NATS
// server (no external deployment) and a NATS client connection for pub/sub.
// Events are JSON-encoded on the wire.
//
// Per ADR 0009, this is the single event/streaming primitive for the management
// plane. The embedded server keeps the deployment single-container.
type NATSBus struct {
	server *natsserver.Server
	nc     *nats.Conn

	mu         sync.Mutex
	subsByType map[string][]*nats.Subscription // for cleanup tracking
}

// NewNATSBus starts an embedded NATS server on a random localhost port and
// returns a NATSBus backed by it. The server runs in-process; no external NATS
// deployment is required. Call Close to shut down.
func NewNATSBus() (*NATSBus, error) {
	// Find a free port to avoid conflicts.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("nats: find free port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	opts := &natsserver.Options{
		Host:   "127.0.0.1",
		Port:   port,
		NoLog:  true,
		NoSigs: true,
	}

	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("nats: create server: %w", err)
	}
	go srv.Start()

	// Wait for server to be ready (client connections block until ready, but
	// the server needs a moment to bind the listener).
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("nats: server did not start in time")
	}

	url := "nats://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	nc, err := nats.Connect(url,
		nats.ReconnectWait(100*time.Millisecond),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		srv.Shutdown()
		return nil, fmt.Errorf("nats: connect: %w", err)
	}

	log.Printf("nats: embedded server listening on %s", url)

	return &NATSBus{
		server:     srv,
		nc:         nc,
		subsByType: make(map[string][]*nats.Subscription),
	}, nil
}

// subject returns the NATS subject for a resource type.
func subject(resourceType string) string {
	return subjectPrefix + resourceType
}

// Publish sends an event to all subscribers of a resource type via NATS.
func (b *NATSBus) Publish(resourceType string, event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("nats: marshal event for %q: %v", resourceType, err)
		return
	}
	if err := b.nc.Publish(subject(resourceType), data); err != nil {
		log.Printf("nats: publish to %q: %v", resourceType, err)
	}
}

// Subscribe registers a watcher for a resource type. Returns a channel that
// receives events and a cancel function (call to unsubscribe).
//
// Internally, this creates a NATS subscription whose handler pushes decoded
// events into a buffered channel, preserving the same consumer API as LocalBus.
func (b *NATSBus) Subscribe(resourceType string) (<-chan Event, context.CancelFunc) {
	ch := make(chan Event, 64)
	subject := subject(resourceType)

	sub, err := b.nc.Subscribe(subject, func(msg *nats.Msg) {
		var ev Event
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			log.Printf("nats: unmarshal event from %q: %v", subject, err)
			return
		}
		select {
		case ch <- ev:
		default:
			// Drop if watcher is too slow (matches LocalBus behaviour).
		}
	})
	if err != nil {
		log.Printf("nats: subscribe to %q: %v", subject, err)
		// Return a closed channel so the consumer exits immediately.
		close(ch)
		_, cancel := context.WithCancel(context.Background())
		cancel()
		return ch, cancel
	}

	b.mu.Lock()
	b.subsByType[resourceType] = append(b.subsByType[resourceType], sub)
	b.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		_ = sub.Unsubscribe()
		close(ch)
	}()

	return ch, cancel
}

// Close shuts down the NATS client connection and the embedded server.
func (b *NATSBus) Close() {
	if b.nc != nil {
		_ = b.nc.Drain()
	}
	if b.server != nil {
		b.server.Shutdown()
	}
}

// NATSAddr returns the address the embedded NATS server is listening on.
// Useful for diagnostics.
func (b *NATSBus) NATSAddr() string {
	if b.server == nil {
		return ""
	}
	return b.server.ClientURL()
}
