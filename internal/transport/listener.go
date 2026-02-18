package transport

import (
	"context"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

type Listener struct {
	addr          string
	ourNodeID     string
	ourListenPort int
	manager       *Manager
}

func NewListener(addr string, ourNodeID string, ourListenPort int, manager *Manager) *Listener {
	return &Listener{
		addr:          addr,
		ourNodeID:     ourNodeID,
		ourListenPort: ourListenPort,
		manager:       manager,
	}
}

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

// Connection ownership transfers to Manager after successful handshake.
func (l *Listener) handleIncoming(raw net.Conn) {
	wrapped := NewConn(raw)
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))

	if err := wrapped.WriteHandshake(Handshake{
		NodeID:     l.ourNodeID,
		ListenPort: l.ourListenPort,
	}); err != nil {
		log.Printf("incoming handshake write: %v", err)
		_ = wrapped.Close()
		return
	}

	peerHS, err := wrapped.ReadHandshake()
	if err != nil {
		log.Printf("incoming handshake read: %v", err)
		_ = wrapped.Close()
		return
	}

	_ = raw.SetDeadline(time.Time{})

	remoteHost, _, _ := net.SplitHostPort(raw.RemoteAddr().String())
	if idx := strings.Index(remoteHost, "%"); idx != -1 {
		remoteHost = remoteHost[:idx]
	}
	peerAddr := net.JoinHostPort(remoteHost, strconv.Itoa(peerHS.ListenPort))

	l.manager.RegisterIncoming(wrapped, peerAddr, peerHS.NodeID)
}
