package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"mesh/internal/discovery"
	"mesh/internal/election"
	"mesh/internal/transport"
)

const defaultPort = 9876

type transportAdapter struct {
	mgr *transport.Manager
}

func (t *transportAdapter) Broadcast(msgType transport.MsgType, payload []byte) error {
	return t.mgr.Broadcast(msgType, payload)
}

func (t *transportAdapter) Send(nodeID string, msgType transport.MsgType, payload []byte) error {
	return t.mgr.SendTo(nodeID, msgType, payload)
}

func (t *transportAdapter) Peers() []string {
	return t.mgr.Peers()
}

func main() {
	port := flag.Int("port", defaultPort, "TCP listen port")
	nodeID := flag.String("node-id", "", "Node ID (must be unique per host)")
	flag.Parse()

	if *nodeID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "mesh-node"
		}
		*nodeID = hostname
	}

	log.Printf("[main] nodeID=%q port=%d", *nodeID, *port)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	manager := transport.NewManager(*nodeID, *port)
	defer manager.Close()

	elec := election.New(
		election.DefaultConfig(*nodeID),
		election.Callbacks{
			OnLeaderElected: func(leaderID string) {
				if leaderID == *nodeID {
					log.Printf("[main] I am now the LEADER")
				} else {
					log.Printf("[main] Leader elected: %s", leaderID)
				}
			},
			OnLeaderLost: func() {
				log.Printf("[main] Lost leadership")
			},
		},
		&transportAdapter{mgr: manager},
	)

	manager.SetMessageHandler(elec.HandleMessage)
	manager.SetPeerCallbacks(elec.AddPeer, elec.RemovePeer)

	onPeer := func(peer transport.Peer) {
		log.Printf("[main] peer discovered: nodeID=%q addr=%s", peer.NodeID, peer.Addr)
		manager.AddPeer(peer)
	}

	disc := discovery.New(*nodeID, *port, onPeer)
	go func() {
		if err := disc.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[main] discovery error: %v", err)
		}
	}()

	listenAddr := ":" + strconv.Itoa(*port)
	listener := transport.NewListener(listenAddr, *nodeID, *port, manager)
	go func() {
		if err := listener.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[main] listener error: %v", err)
		}
	}()

	go elec.Start(ctx)

	log.Printf("[main] mesh node %q listening on %s", *nodeID, listenAddr)
	<-ctx.Done()
	log.Println("[main] shutting down...")
	cancel()
	time.Sleep(1 * time.Second)
}
