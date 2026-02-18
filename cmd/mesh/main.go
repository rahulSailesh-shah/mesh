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
	"mesh/internal/transport"
)

const defaultPort = 9876

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

	go manager.RunHealthCheck(ctx)

	log.Printf("[main] mesh node %q listening on %s", *nodeID, listenAddr)
	<-ctx.Done()
	log.Println("[main] shutting down...")
	cancel()
	time.Sleep(1 * time.Second)
}
