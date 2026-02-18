package transport

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
)

const handshakeTimeout = 5 * time.Second

type Peer struct {
	NodeID string
	Addr   string
}

type peerState struct {
	addr string
	conn *Conn
}

type MessageHandler func(from string, msgType MsgType, payload []byte)
type PeerCallback func(nodeID string)

type Manager struct {
	ourNodeID     string
	ourListenPort int
	peers         map[string]*peerState
	mu            sync.RWMutex

	onMessage   MessageHandler
	onPeerAdded PeerCallback
	onPeerLost  PeerCallback
	onData      MessageHandler
}

func NewManager(ourNodeID string, ourListenPort int) *Manager {
	return &Manager{
		ourNodeID:     ourNodeID,
		ourListenPort: ourListenPort,
		peers:         make(map[string]*peerState),
	}
}

func (m *Manager) SetMessageHandler(h MessageHandler) {
	m.onMessage = h
}

func (m *Manager) SetPeerCallbacks(onAdded, onLost PeerCallback) {
	m.onPeerAdded = onAdded
	m.onPeerLost = onLost
}

func (m *Manager) SetDataHandler(h MessageHandler) {
	m.onData = h
}

func (m *Manager) AddPeer(peer Peer) {
	if peer.NodeID == "" || peer.NodeID == m.ourNodeID {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.getOrCreate(peer.NodeID, peer.Addr)
	if ps.addr != peer.Addr {
		log.Printf("[transport] peer %s changed address: %s -> %s", peer.NodeID, ps.addr, peer.Addr)
		ps.addr = peer.Addr
	}

	if ps.conn == nil && m.ourNodeID < peer.NodeID {
		go m.connectToPeer(peer)
	}
}

func (m *Manager) getOrCreate(nodeID, addr string) *peerState {
	if ps, ok := m.peers[nodeID]; ok {
		return ps
	}
	ps := &peerState{addr: addr}
	m.peers[nodeID] = ps
	if m.onPeerAdded != nil {
		go m.onPeerAdded(nodeID)
	}
	return ps
}

func (m *Manager) connectToPeer(peer Peer) {
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.Dial("tcp", peer.Addr)
	if err != nil {
		log.Printf("[transport] dial %s (%s): %v", peer.Addr, peer.NodeID, err)
		return
	}
	wrapped := NewConn(raw)
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := wrapped.WriteHandshake(Handshake{NodeID: m.ourNodeID, ListenPort: m.ourListenPort}); err != nil {
		_ = wrapped.Close()
		log.Printf("[transport] handshake write to %s: %v", peer.NodeID, err)
		return
	}
	if _, err := wrapped.ReadHandshake(); err != nil {
		_ = wrapped.Close()
		log.Printf("[transport] handshake read from %s: %v", peer.NodeID, err)
		return
	}
	_ = raw.SetDeadline(time.Time{})

	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.peers[peer.NodeID]
	if !ok {
		_ = wrapped.Close()
		return
	}
	if ps.conn != nil {
		_ = wrapped.Close()
		return
	}
	ps.conn = wrapped
	go m.readLoop(peer.NodeID, wrapped)
	log.Printf("[transport] established outbound connection to %s", peer.NodeID)
}

func (m *Manager) RegisterIncoming(conn *Conn, peerAddr, nodeID string) {
	if nodeID == "" || nodeID == m.ourNodeID {
		_ = conn.Close()
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.getOrCreate(nodeID, peerAddr)
	if ps.conn != nil {
		_ = ps.conn.Close()
	}
	ps.conn = conn
	go m.readLoop(nodeID, conn)
	log.Printf("[transport] accepted connection from %s (%s)", peerAddr, nodeID)
}

func (m *Manager) readLoop(nodeID string, c *Conn) {
	log.Printf("[transport] read loop started for %s", nodeID)
	defer func() {
		log.Printf("[transport] read loop ended for %s", nodeID)
		_ = c.Close()
		m.mu.Lock()
		if ps, ok := m.peers[nodeID]; ok && ps.conn == c {
			ps.conn = nil
			delete(m.peers, nodeID)
		}
		m.mu.Unlock()
		if m.onPeerLost != nil {
			m.onPeerLost(nodeID)
		}
	}()

	for {
		t, payload, err := c.ReadMessage()
		if err != nil {
			log.Printf("[transport] read error from %s: %v", nodeID, err)
			return
		}

		switch t {
		case MsgHeartbeat:
			ackPayload, _ := json.Marshal(struct{}{})
			_ = c.WriteMessage(MsgHeartbeatAck, ackPayload)
			if m.onMessage != nil {
				m.onMessage(nodeID, t, payload)
			}
		case MsgElection, MsgCoordinator:
			if m.onMessage != nil {
				m.onMessage(nodeID, t, payload)
			}
		case MsgData:
			if m.onData != nil {
				m.onData(nodeID, t, payload)
			}
		}
	}
}

func (m *Manager) Broadcast(msgType MsgType, payload []byte) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var lastErr error
	for nodeID, ps := range m.peers {
		if ps.conn != nil {
			if err := ps.conn.WriteMessage(msgType, payload); err != nil {
				log.Printf("[transport] broadcast to %s failed: %v", nodeID, err)
				lastErr = err
			}
		}
	}
	return lastErr
}

func (m *Manager) SendTo(nodeID string, msgType MsgType, payload []byte) error {
	m.mu.RLock()
	ps, ok := m.peers[nodeID]
	m.mu.RUnlock()
	if !ok || ps.conn == nil {
		return nil
	}
	return ps.conn.WriteMessage(msgType, payload)
}

func (m *Manager) Peers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peers := make([]string, 0, len(m.peers))
	for nodeID := range m.peers {
		peers = append(peers, nodeID)
	}
	return peers
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
