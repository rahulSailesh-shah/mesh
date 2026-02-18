package transport

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

const maxMessageSize = 4 * 1024 * 1024 // 4 MiB

// MsgType identifies the purpose of a message.
type MsgType byte

const (
	MsgHandshake MsgType = iota
	MsgPing
	MsgPong
	MsgData
)

// Handshake is exchanged immediately after TCP connect.
type Handshake struct {
	NodeID     string `json:"node_id"`
	ListenPort int    `json:"listen_port"`
}

// Conn wraps a net.Conn with length-prefixed message framing.
// Protocol: [4-byte length][1-byte type][payload...]
// The length includes the type byte.
type Conn struct {
	net.Conn
	mu sync.Mutex
}

// NewConn wraps c with framing.
func NewConn(c net.Conn) *Conn {
	return &Conn{Conn: c}
}

// WriteMessage writes len(payload)+1 as 4-byte big-endian then MsgType then payload.
func (c *Conn) WriteMessage(t MsgType, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(payload)+1))
	if _, err := c.Conn.Write(lenBuf); err != nil {
		return err
	}
	if _, err := c.Conn.Write([]byte{byte(t)}); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.Conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadMessage reads 4-byte length followed by type byte and payload.
func (c *Conn) ReadMessage() (MsgType, []byte, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(c.Conn, lenBuf); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf)
	if n == 0 {
		return 0, nil, fmt.Errorf("zero length message")
	}
	if n > maxMessageSize {
		return 0, nil, io.ErrShortBuffer
	}

	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(c.Conn, typeBuf); err != nil {
		return 0, nil, err
	}

	payloadLen := n - 1
	if payloadLen == 0 {
		return MsgType(typeBuf[0]), nil, nil
	}

	buf := make([]byte, payloadLen)
	if _, err := io.ReadFull(c.Conn, buf); err != nil {
		return 0, nil, err
	}
	return MsgType(typeBuf[0]), buf, nil
}

// WriteHandshake JSON-encodes h and sends it as a MsgHandshake.
func (c *Conn) WriteHandshake(h Handshake) error {
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("marshal handshake: %w", err)
	}
	return c.WriteMessage(MsgHandshake, data)
}

// ReadHandshake reads one framed message and JSON-decodes it into a Handshake.
func (c *Conn) ReadHandshake() (Handshake, error) {
	t, data, err := c.ReadMessage()
	if err != nil {
		return Handshake{}, err
	}
	if t != MsgHandshake {
		return Handshake{}, fmt.Errorf("expected MsgHandshake, got %v", t)
	}
	var h Handshake
	if err := json.Unmarshal(data, &h); err != nil {
		return Handshake{}, fmt.Errorf("unmarshal handshake: %w", err)
	}
	return h, nil
}
