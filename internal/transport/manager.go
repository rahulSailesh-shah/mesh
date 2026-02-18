package transport

import (
	"context"
	"log"
	"net"
	"sync"
	"time"
)

const (
	dialInitialBackoff = 1 * time.Second
	dialMaxBackoff     = 60 * time.Second
	probeInterval      = 10 * time.Second
	handshakeTimeout   = 5 * time.Second
	maxFailedAttempts  = 3
)

type Peer struct {
	NodeID string
	Addr   string
}

type peerState struct {
	addr           string
	conn           *Conn
	backoff        time.Duration
	lastDialed     time.Time
	failedAttempts int
}

type Manager struct {
	ourNodeID     string
	ourListenPort int
	peers         map[string]*peerState
	mu            sync.RWMutex
}

func NewManager(ourNodeID string, ourListenPort int) *Manager {
	return &Manager{
		ourNodeID:     ourNodeID,
		ourListenPort: ourListenPort,
		peers:         make(map[string]*peerState),
	}
}

func (m *Manager) AddPeer(peer Peer) {
	if peer.NodeID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.getOrCreate(peer.NodeID, peer.Addr)
	if ps.addr != peer.Addr {
		log.Printf("peer %s changed address: %s -> %s", peer.NodeID, ps.addr, peer.Addr)
		ps.addr = peer.Addr
	}
}

func (m *Manager) getOrCreate(nodeID, addr string) *peerState {
	if ps, ok := m.peers[nodeID]; ok {
		return ps
	}
	ps := &peerState{addr: addr, backoff: dialInitialBackoff}
	m.peers[nodeID] = ps
	return ps
}

func (m *Manager) RegisterIncoming(conn *Conn, peerAddr, nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.getOrCreate(nodeID, peerAddr)
	if ps.conn != nil {
		_ = ps.conn.Close()
	}
	ps.conn = conn
	ps.failedAttempts = 0
	go m.readLoop(nodeID, conn)
	log.Printf("accepted connection from %s (%s)", peerAddr, nodeID)
}

func (m *Manager) readLoop(nodeID string, c *Conn) {
	log.Printf("read loop started for %s", nodeID)
	defer func() {
		log.Printf("read loop ended for %s", nodeID)
		m.mu.Lock()
		if ps, ok := m.peers[nodeID]; ok {
			ps.conn = nil
			_ = c.Close()
			if m.ourNodeID > nodeID {
				delete(m.peers, nodeID)
				log.Printf("evicted peer %s (passive side, connection lost)", nodeID)
			}
		}
		m.mu.Unlock()
	}()
	for {
		t, _, err := c.ReadMessage()
		if err != nil {
			log.Printf("read error from %s: %v", nodeID, err)
			return
		}
		if t == MsgPing {
			log.Printf("received ping from %s, sending pong", nodeID)
			_ = c.WriteMessage(MsgPong, nil)
		}
	}
}

func (m *Manager) RunHealthCheck(ctx context.Context) {
	log.Printf("health check loop started (interval: %v)", probeInterval)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("health check loop stopping: context cancelled")
			return
		case <-ticker.C:
			m.probePeers()
		}
	}
}

func (m *Manager) probePeers() {
	m.mu.RLock()
	peers := make([]Peer, 0, len(m.peers))
	for id, ps := range m.peers {
		peers = append(peers, Peer{NodeID: id, Addr: ps.addr})
	}
	m.mu.RUnlock()
	log.Printf("probing %d peer(s)", len(peers))

	for _, p := range peers {
		m.probeOne(p)
	}
}

func (m *Manager) probeOne(peer Peer) {
	// Lower NodeID dials to prevent duplicate connections between peers.
	if m.ourNodeID >= peer.NodeID {
		log.Printf("skipping probe to %s: higher NodeID (ours=%s, theirs=%s)", peer.NodeID, m.ourNodeID, peer.NodeID)
		return
	}

	m.mu.RLock()
	ps, ok := m.peers[peer.NodeID]
	if !ok {
		m.mu.RUnlock()
		log.Printf("peer %s no longer exists, skipping probe", peer.NodeID)
		return
	}
	conn, lastDial, bo := ps.conn, ps.lastDialed, ps.backoff
	m.mu.RUnlock()

	if conn != nil {
		log.Printf("sending ping to %s", peer.NodeID)
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
		err := conn.WriteMessage(MsgPing, nil)
		_ = conn.SetDeadline(time.Time{})
		if err != nil {
			log.Printf("ping failed for %s: %v", peer.NodeID, err)
			_ = conn.Close()
			m.mu.Lock()
			if ps, ok := m.peers[peer.NodeID]; ok && ps.conn == conn {
				ps.conn = nil
			}
			m.mu.Unlock()
			m.markFailure(peer.NodeID)
		} else {
			log.Printf("ping succeeded to %s", peer.NodeID)
		}
		return
	}

	if time.Since(lastDial) < bo {
		log.Printf("backing off dial to %s (waited %v, need %v)", peer.NodeID, time.Since(lastDial), bo)
		return
	}

	log.Printf("attempting dial to %s (%s)", peer.Addr, peer.NodeID)

	m.mu.Lock()
	ps.lastDialed = time.Now()
	m.mu.Unlock()

	newConn, err := m.dial(peer)
	if err != nil {
		log.Printf("dial %s (%s): %v", peer.Addr, peer.NodeID, err)
		m.markFailure(peer.NodeID)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok = m.peers[peer.NodeID]
	if !ok || ps.conn != nil {
		_ = newConn.Close()
		return
	}
	ps.conn = newConn
	ps.backoff = dialInitialBackoff
	ps.failedAttempts = 0
	go m.readLoop(peer.NodeID, newConn)
	log.Printf("established outbound connection to %s", peer.NodeID)
}

func (m *Manager) dial(peer Peer) (*Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.Dial("tcp", peer.Addr)
	if err != nil {
		return nil, err
	}
	wrapped := NewConn(raw)
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := wrapped.WriteHandshake(Handshake{NodeID: m.ourNodeID, ListenPort: m.ourListenPort}); err != nil {
		_ = wrapped.Close()
		return nil, err
	}
	if _, err := wrapped.ReadHandshake(); err != nil {
		_ = wrapped.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return wrapped, nil
}

func (m *Manager) markFailure(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.peers[nodeID]
	if !ok {
		return
	}
	ps.failedAttempts++
	ps.backoff = min(ps.backoff*2, dialMaxBackoff)
	if ps.failedAttempts >= maxFailedAttempts {
		log.Printf("evicting peer %s after %d failed probes", nodeID, maxFailedAttempts)
		if ps.conn != nil {
			_ = ps.conn.Close()
		}
		delete(m.peers, nodeID)
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for nodeID, ps := range m.peers {
		if ps.conn != nil {
			_ = ps.conn.Close()
		}
		delete(m.peers, nodeID)
	}
}
