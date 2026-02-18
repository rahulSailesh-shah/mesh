package transport

import (
	"context"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

// Listener listens on a TCP port and hands accepted connections to the manager.
type Listener struct {
	addr          string
	ourNodeID     string
	ourListenPort int
	manager       *Manager
}

// NewListener creates a listener that will pass accepted conns to manager.
func NewListener(addr string, ourNodeID string, ourListenPort int, manager *Manager) *Listener {
	return &Listener{
		addr:          addr,
		ourNodeID:     ourNodeID,
		ourListenPort: ourListenPort,
		manager:       manager,
	}
}

// Run blocks until ctx is canceled. Accepts connections, performs a handshake,
// then registers them with the manager.
func (l *Listener) Run(ctx context.Context) error {
	lc := net.ListenConfig{KeepAlive: 30 * time.Second}
	listen, err := lc.Listen(ctx, "tcp", l.addr)
	if err != nil {
		return err
	}
	defer listen.Close()

	go func() {
		<-ctx.Done()
		_ = listen.Close()
	}()

	for ctx.Err() == nil {
		raw, err := listen.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("accept error: %v", err)
			continue
		}
		go l.handleIncoming(raw)
	}
	return nil
}

// handleIncoming performs the handshake and registers the connection.
func (l *Listener) handleIncoming(raw net.Conn) {
	// Do not attempt to close it as a part of defer, since the manager will do that on failing to connect after max retries.
	wrapped := NewConn(raw)

	// Set a deadline for the handshake exchange.
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))

	// Send our handshake first.
	if err := wrapped.WriteHandshake(Handshake{
		NodeID:     l.ourNodeID,
		ListenPort: l.ourListenPort,
	}); err != nil {
		log.Printf("incoming handshake write: %v", err)
		_ = wrapped.Close()
		return
	}

	// Read the peer's handshake.
	peerHS, err := wrapped.ReadHandshake()
	if err != nil {
		log.Printf("incoming handshake read: %v", err)
		_ = wrapped.Close()
		return
	}

	// Clear deadline for normal operation.
	_ = raw.SetDeadline(time.Time{})

	// Reconstruct the peer's listen address.
	remoteHost, _, _ := net.SplitHostPort(raw.RemoteAddr().String())
	if idx := strings.Index(remoteHost, "%"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	peerAddr := net.JoinHostPort(remoteHost, strconv.Itoa(peerHS.ListenPort))

	l.manager.RegisterIncoming(wrapped, peerAddr, peerHS.NodeID)
}
