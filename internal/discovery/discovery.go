// Package discovery uses mDNS/Zeroconf for peer discovery on the local network.
package discovery

import (
	"context"
	"log"
	"net"
	"strconv"

	"mesh/internal/transport"

	"github.com/grandcat/zeroconf"
)

const (
	serviceType = "_mesh._tcp"
	domain      = "local."
)

type Discovery struct {
	ourNodeID string
	ourPort   int
	onPeer    func(peer transport.Peer)
}

func New(ourNodeID string, ourPort int, onPeer func(peer transport.Peer)) *Discovery {
	return &Discovery{
		ourNodeID: ourNodeID,
		ourPort:   ourPort,
		onPeer:    onPeer,
	}
}

func (d *Discovery) Start(ctx context.Context) error {
	meta := []string{
		"version=0.1.0",
		"hello=world",
	}
	server, err := zeroconf.Register(d.ourNodeID, serviceType, domain, d.ourPort, meta, nil)
	if err != nil {
		return err
	}
	defer server.Shutdown()
	log.Printf("[discovery] registered %q on port %d", d.ourNodeID, d.ourPort)

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		for entry := range entries {
			if entry == nil || entry.Instance == d.ourNodeID {
				continue
			}
			log.Printf("[discovery] found: instance=%q ipv4=%v port=%d", entry.Instance, entry.AddrIPv4, entry.Port)

			if len(entry.AddrIPv4) == 0 {
				log.Printf("[discovery] no IPv4 for %q, skipping", entry.Instance)
				continue
			}

			peer := transport.Peer{
				NodeID: entry.Instance,
				Addr:   net.JoinHostPort(entry.AddrIPv4[0].String(), strconv.Itoa(entry.Port)),
			}
			d.onPeer(peer)
		}
	}()

	log.Printf("[discovery] browsing for %s", serviceType)
	if err := resolver.Browse(ctx, serviceType, domain, entries); err != nil {
		return err
	}

	<-ctx.Done()
	log.Printf("[discovery] shutting down")
	return nil
}
