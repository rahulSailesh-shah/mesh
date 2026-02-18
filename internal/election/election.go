package election

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"sync"
	"time"

	"mesh/internal/transport"
)

type Transport interface {
	Broadcast(msgType transport.MsgType, payload []byte) error
	Send(nodeID string, msgType transport.MsgType, payload []byte) error
	Peers() []string
}

type Heartbeat struct {
	Term     uint64 `json:"term"`
	LeaderID string `json:"leader_id"`
}

type ElectionMsg struct {
	Term   uint64 `json:"term"`
	NodeID string `json:"node_id"`
}

type Coordinator struct {
	Term     uint64 `json:"term"`
	LeaderID string `json:"leader_id"`
}

type Election struct {
	config    Config
	callbacks Callbacks
	transport Transport

	mu           sync.RWMutex
	state        State
	term         uint64
	leaderID     string
	peers        map[string]struct{}
	lastHB       time.Time
	stopElection chan struct{}

	rand *rand.Rand
}

func New(config Config, callbacks Callbacks, transport Transport) *Election {
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = 6 * time.Second
	}
	if config.ElectionTimeout == 0 {
		config.ElectionTimeout = 5 * time.Second
	}
	if config.HeartbeatTick == 0 {
		config.HeartbeatTick = 2 * time.Second
	}

	return &Election{
		config:       config,
		callbacks:    callbacks,
		transport:    transport,
		state:        StateFollower,
		peers:        make(map[string]struct{}),
		lastHB:       time.Now(),
		rand:         rand.New(rand.NewSource(time.Now().UnixNano())),
		stopElection: make(chan struct{}),
	}
}

func (e *Election) Start(ctx context.Context) error {
	log.Printf("[election] starting, nodeID=%s", e.config.NodeID)

	go e.leaderMonitor(ctx)
	go e.heartbeatSender(ctx)

	<-ctx.Done()
	return ctx.Err()
}

func (e *Election) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state == StateLeader
}

func (e *Election) CurrentLeader() (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.leaderID, e.leaderID != ""
}

func (e *Election) State() State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

func (e *Election) Term() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.term
}

func (e *Election) AddPeer(nodeID string) {
	if nodeID == "" || nodeID == e.config.NodeID {
		return
	}
	e.mu.Lock()
	e.peers[nodeID] = struct{}{}
	log.Printf("[election] peer added: %s (total peers: %d)", nodeID, len(e.peers))
	e.mu.Unlock()
}

func (e *Election) RemovePeer(nodeID string) {
	e.mu.Lock()
	delete(e.peers, nodeID)
	log.Printf("[election] peer removed: %s (remaining peers: %d)", nodeID, len(e.peers))
	e.mu.Unlock()
}

func (e *Election) HandleMessage(from string, msgType transport.MsgType, payload []byte) {
	switch msgType {
	case transport.MsgHeartbeat:
		var hb Heartbeat
		if err := json.Unmarshal(payload, &hb); err != nil {
			log.Printf("[election] failed to unmarshal heartbeat: %v", err)
			return
		}
		e.handleHeartbeat(from, hb)
	case transport.MsgElection:
		var em ElectionMsg
		if err := json.Unmarshal(payload, &em); err != nil {
			log.Printf("[election] failed to unmarshal election msg: %v", err)
			return
		}
		e.handleElectionMsg(from, em)
	case transport.MsgCoordinator:
		var coord Coordinator
		if err := json.Unmarshal(payload, &coord); err != nil {
			log.Printf("[election] failed to unmarshal coordinator: %v", err)
			return
		}
		e.handleCoordinator(from, coord)
	default:
		log.Printf("[election] unknown message type: %v", msgType)
	}

}

func (e *Election) handleHeartbeat(from string, hb Heartbeat) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if hb.Term < e.term {
		log.Printf("[election] ignoring stale heartbeat from %s (term %d < %d)", from, hb.Term, e.term)
		return
	}

	if hb.Term > e.term {
		e.term = hb.Term
	}

	if e.state == StateLeader && hb.Term == e.term && hb.LeaderID != e.config.NodeID {
		if hb.LeaderID > e.config.NodeID {
			log.Printf("[election] stepping down: higher priority leader %s", hb.LeaderID)
			e.stepDownLocked(hb.LeaderID)
		}
		return
	}

	if e.state != StateFollower {
		e.state = StateFollower
		if e.leaderID != hb.LeaderID && e.callbacks.OnLeaderLost != nil {
			e.callbacks.OnLeaderLost()
		}
	}

	oldLeader := e.leaderID
	e.leaderID = hb.LeaderID
	e.lastHB = time.Now()

	if oldLeader != hb.LeaderID {
		log.Printf("[election] leader is now: %s (term %d)", hb.LeaderID, hb.Term)
		if e.callbacks.OnLeaderElected != nil {
			e.callbacks.OnLeaderElected(hb.LeaderID)
		}
	}
}

func (e *Election) handleElectionMsg(from string, em ElectionMsg) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if em.Term > e.term {
		e.term = em.Term
		if e.state == StateLeader {
			log.Printf("[election] stepping down due to higher term election from %s", from)
			e.stepDownLocked("")
		}
	}

	if em.NodeID > e.config.NodeID {
		log.Printf("[election] received election from higher priority %s, deferring", from)
		return
	}

	go func() {
		resp := ElectionMsg{Term: e.term, NodeID: e.config.NodeID}
		data, _ := json.Marshal(resp)
		if err := e.transport.Send(from, transport.MsgElection, data); err != nil {
			log.Printf("[election] failed to send election response to %s: %v", from, err)
		}
	}()
}

func (e *Election) handleCoordinator(from string, coord Coordinator) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if coord.Term < e.term {
		log.Printf("[election] ignoring stale coordinator from %s (term %d < %d)", from, coord.Term, e.term)
		return
	}

	if coord.Term > e.term {
		e.term = coord.Term
	}

	oldLeader := e.leaderID
	oldState := e.state

	e.leaderID = coord.LeaderID
	e.state = StateFollower
	e.lastHB = time.Now()

	if oldState == StateLeader && oldLeader == e.config.NodeID && e.callbacks.OnLeaderLost != nil {
		e.callbacks.OnLeaderLost()
	}

	if oldLeader != coord.LeaderID {
		log.Printf("[election] coordinator received: leader=%s term=%d", coord.LeaderID, coord.Term)
		if e.callbacks.OnLeaderElected != nil {
			e.callbacks.OnLeaderElected(coord.LeaderID)
		}
	}
}

func (e *Election) stepDownLocked(newLeader string) {
	wasLeader := e.state == StateLeader
	e.state = StateFollower
	if newLeader != "" {
		e.leaderID = newLeader
	}
	if wasLeader && e.callbacks.OnLeaderLost != nil {
		go e.callbacks.OnLeaderLost()
	}
}

func (e *Election) leaderMonitor(ctx context.Context) {
	ticker := time.NewTicker(e.config.HeartbeatTimeout / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.checkLeaderTimeout()
		}
	}
}

func (e *Election) checkLeaderTimeout() {
	e.mu.Lock()
	lastHB := e.lastHB
	state := e.state
	e.mu.Unlock()

	if state == StateLeader {
		return
	}

	if time.Since(lastHB) > e.config.HeartbeatTimeout {
		log.Printf("[election] leader timeout (last heartbeat %v ago)", time.Since(lastHB))
		e.startElection()
	}
}

func (e *Election) startElection() {
	jitter := time.Duration(e.rand.Intn(500)) * time.Millisecond
	time.Sleep(jitter)

	e.mu.Lock()
	if e.state == StateLeader {
		e.mu.Unlock()
		return
	}

	e.term++
	e.state = StateCandidate
	currentTerm := e.term
	peers := make([]string, 0, len(e.peers))
	for p := range e.peers {
		peers = append(peers, p)
	}
	e.mu.Unlock()

	log.Printf("[election] starting election, term=%d, peers=%d", currentTerm, len(peers))

	em := ElectionMsg{Term: currentTerm, NodeID: e.config.NodeID}
	data, _ := json.Marshal(em)
	_ = e.transport.Broadcast(transport.MsgElection, data)

	time.Sleep(e.config.ElectionTimeout)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.state != StateCandidate {
		log.Printf("[election] no longer candidate, state=%s", e.state)
		return
	}

	higherPriority := false
	for p := range e.peers {
		if p > e.config.NodeID {
			higherPriority = true
			break
		}
	}

	if !higherPriority {
		log.Printf("[election] becoming leader, term=%d", e.term)
		e.state = StateLeader
		e.leaderID = e.config.NodeID
		if e.callbacks.OnLeaderElected != nil {
			go e.callbacks.OnLeaderElected(e.config.NodeID)
		}

		coord := Coordinator{Term: e.term, LeaderID: e.config.NodeID}
		coordData, _ := json.Marshal(coord)
		go e.transport.Broadcast(transport.MsgCoordinator, coordData)
	} else {
		log.Printf("[election] higher priority peers exist, waiting")
	}
}

func (e *Election) heartbeatSender(ctx context.Context) {
	ticker := time.NewTicker(e.config.HeartbeatTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.sendHeartbeat()
		}
	}
}

func (e *Election) sendHeartbeat() {
	e.mu.RLock()
	isLeader := e.state == StateLeader
	term := e.term
	e.mu.RUnlock()

	if !isLeader {
		return
	}

	hb := Heartbeat{Term: term, LeaderID: e.config.NodeID}
	data, _ := json.Marshal(hb)
	if err := e.transport.Broadcast(transport.MsgHeartbeat, data); err != nil {
		log.Printf("[election] failed to broadcast heartbeat: %v", err)
	}
}
