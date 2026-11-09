package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocket tuning. A client that does not answer pings within pongWait is
// dropped, which is how a crashed game leaves its room.
const (
	writeWait    = 5 * time.Second
	pongWait     = 40 * time.Second
	pingPeriod   = 25 * time.Second
	maxFrameSize = 256 * 1024
	sendQueue    = 1024
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// serveWS upgrades the HTTP request and runs the client until its socket
// closes. The read loop runs on this goroutine; writes get their own.
func serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &Client{conn: conn, send: make(chan outgoing, sendQueue)}
	go c.writeLoop()
	c.readLoop()
}

// readLoop dispatches incoming frames until the connection fails, then
// removes the client from its room.
func (c *Client) readLoop() {
	defer reg.leave(c)
	defer c.close()
	c.conn.SetReadLimit(maxFrameSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case websocket.TextMessage:
			c.handleText(data)
		case websocket.BinaryMessage:
			c.handleBinary(data)
		}
	}
}

// writeLoop is the only goroutine that writes to the socket. It drains the
// send queue and keeps the connection alive with pings.
func (c *Client) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer c.close()
	for {
		select {
		case out, ok := <-c.send:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(out.kind, out.data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// close shuts the socket once. Both loops exit on their next operation.
func (c *Client) close() {
	c.once.Do(func() { _ = c.conn.Close() })
}

// queue puts a frame on the client's send queue without ever blocking, so it
// is safe to call with Registry.mu held. Snapshots are dropped when the queue
// is full because a newer one is on its way. Anything else that does not fit
// means the client is hopelessly behind, and the connection is closed instead
// of stalling the whole relay.
func (c *Client) queue(kind int, data []byte) {
	select {
	case c.send <- outgoing{kind: kind, data: data}:
		return
	default:
	}
	if kind == websocket.BinaryMessage && len(data) >= 2 && data[1] == kindSnapshot {
		return
	}
	log.Printf("player %d: send queue full, dropping connection", c.id)
	c.close()
}

func (c *Client) sendJSON(m Message) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.queue(websocket.TextMessage, b)
}

// handleText parses one control message and acts on it.
func (c *Client) handleText(data []byte) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		c.sendJSON(errorMessage("bad json"))
		return
	}
	if (m.T == "host" || m.T == "join") && m.V != ProtocolVersion {
		c.sendJSON(errorMessage(versionMismatch(m.V)))
		return
	}
	switch m.T {
	case "host":
		reg.host(c)
	case "join":
		reg.join(c, m.Code)
	case "leave":
		reg.leave(c)
	case "start":
		reg.start(c, m)
	case "msg":
		reg.forward(c, m)
	case "ping":
		c.sendJSON(Message{T: "pong"})
	default:
		c.sendJSON(errorMessage("unknown type " + m.T))
	}
}

// handleBinary forwards a game frame that arrived over the WebSocket, which
// happens before UDP is confirmed or when UDP is blocked for this player.
// The incoming routing byte is the target; it is replaced by the sender id.
func (c *Client) handleBinary(data []byte) {
	if len(data) < 1 {
		return
	}
	reg.route(c, int(data[0]), data[1:])
}

func versionMismatch(theirs int) string {
	if theirs < ProtocolVersion {
		return fmt.Sprintf("Your game is out of date (server protocol %d, yours %d). Please update Roomshift.", ProtocolVersion, theirs)
	}
	return fmt.Sprintf("This room server is out of date (server protocol %d, yours %d).", ProtocolVersion, theirs)
}
