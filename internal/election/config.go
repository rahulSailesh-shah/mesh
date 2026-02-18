package election

import (
	"time"
)

type Config struct {
	NodeID           string
	HeartbeatTimeout time.Duration
	ElectionTimeout  time.Duration
	HeartbeatTick    time.Duration
}

func DefaultConfig(nodeID string) Config {
	return Config{
		NodeID:           nodeID,
		HeartbeatTimeout: 6 * time.Second,
		ElectionTimeout:  5 * time.Second,
		HeartbeatTick:    2 * time.Second,
	}
}

type Callbacks struct {
	OnLeaderElected func(leaderID string)
	OnLeaderLost    func()
}

type State int

const (
	StateFollower State = iota
	StateCandidate
	StateLeader
)

func (s State) String() string {
	switch s {
	case StateFollower:
		return "follower"
	case StateCandidate:
		return "candidate"
	case StateLeader:
		return "leader"
	default:
		return "unknown"
	}
}
