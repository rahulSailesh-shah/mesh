// Package transport provides TCP-based mesh networking with length-prefixed framing.
// Wire protocol: [4-byte BE length][1-byte type][payload]
package transport

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

const maxMessageSize = 4 * 1024 * 1024

type MsgType byte

const (
	MsgHandshake MsgType = iota
	MsgPing
	MsgPong
	MsgData
)

type Handshake struct {
	NodeID     string `json:"node_id"`
	ListenPort int    `json:"listen_port"`
}

type Conn struct {
	net.Conn
	mu sync.Mutex
}

func NewConn(c net.Conn) *Conn {
	return &Conn{Conn: c}
}

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

func (c *Conn) WriteHandshake(h Handshake) error {
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("marshal handshake: %w", err)
	}
	return c.WriteMessage(MsgHandshake, data)
}

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
