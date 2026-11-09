package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net"
	"sync"

	"github.com/gorilla/websocket"
)

// Client is one connected game. It exists from the WebSocket upgrade until the
// socket closes, and belongs to at most one Room at a time.
type Client struct {
	conn *websocket.Conn
	send chan outgoing // frames waiting for the write loop; see Client.queue
	once sync.Once

	// Everything below is guarded by Registry.mu.
	id   int          // 1 for the host, 2.. for guests, 0 while not in a room
	room *Room        // nil while not in a room
	key  uint64       // UDP session key; 0 while not in a room
	udp  *net.UDPAddr // where this client's UDP datagrams come from; nil until the first hello
}

// outgoing is one frame queued for a client's WebSocket.
type outgoing struct {
	kind int // websocket.TextMessage or websocket.BinaryMessage
	data []byte
}

// Room is one lobby or run. The host is always member 1; guests get the next
// free id, which is never reused within the room so late messages addressed
// to a departed player go nowhere.
type Room struct {
	code    string
	host    *Client
	members map[int]*Client
	nextID  int
	started bool
}

// Registry holds every room and the UDP key lookup. One mutex covers all of
// it; the work done under the lock is small and never blocks on the network,
// because queue and deliver drop or close instead of waiting.
type Registry struct {
	mu    sync.Mutex
	rooms map[string]*Room
	byKey map[uint64]*Client
}

var reg = &Registry{
	rooms: map[string]*Room{},
	byKey: map[uint64]*Client{},
}

// counts reports how many rooms and players are connected, for /health.
func (g *Registry) counts() (rooms, players int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.rooms {
		players += len(r.members)
	}
	return len(g.rooms), players
}

// host creates a new room with c as player 1 and tells c its code and key.
func (g *Registry) host(c *Client) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.leaveLocked(c)
	code := newCode()
	for g.rooms[code] != nil {
		code = newCode()
	}
	r := &Room{code: code, host: c, members: map[int]*Client{}, nextID: 2}
	g.rooms[code] = r
	g.enterLocked(c, r, 1)
	c.sendJSON(Message{T: "hosted", Code: code, ID: 1, Key: c.keyHex(), UPort: udpPort})
	log.Printf("room %s hosted", code)
}

// join adds c to the room with the given code, or explains why it cannot.
func (g *Registry) join(c *Client, code string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.rooms[code]
	if r == nil {
		c.sendJSON(errorMessage("no such room"))
		return
	}
	if len(r.members) >= MaxPlayers {
		c.sendJSON(errorMessage("room full"))
		return
	}
	g.leaveLocked(c)
	id := r.nextID
	r.nextID++
	g.enterLocked(c, r, id)
	peers := make([]int, 0, len(r.members))
	for other := range r.members {
		if other != id {
			peers = append(peers, other)
		}
	}
	c.sendJSON(Message{T: "joined", Code: code, ID: id, Peers: peers, Host: r.host.id, Key: c.keyHex(), UPort: udpPort})
	r.broadcastJSON(c, Message{T: "peer_joined", ID: id})
	log.Printf("room %s: player %d joined (%d in room)", code, id, len(r.members))
}

// enterLocked registers c as member id of r and issues a fresh UDP key.
func (g *Registry) enterLocked(c *Client, r *Room, id int) {
	c.id = id
	c.room = r
	c.udp = nil
	c.key = newKey()
	r.members[id] = c
	g.byKey[c.key] = c
}

// leave removes c from its room, if any. Safe to call more than once.
func (g *Registry) leave(c *Client) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.leaveLocked(c)
}

// leaveLocked is leave with Registry.mu already held. When the host leaves
// the whole room closes and every guest is told so.
func (g *Registry) leaveLocked(c *Client) {
	r := c.room
	if r == nil {
		return
	}
	delete(r.members, c.id)
	delete(g.byKey, c.key)
	c.room = nil
	c.key = 0
	c.udp = nil
	if r.host == c {
		for _, other := range r.members {
			other.sendJSON(Message{T: "closed", Why: "host left"})
			delete(g.byKey, other.key)
			other.room = nil
			other.key = 0
			other.udp = nil
		}
		delete(g.rooms, r.code)
		log.Printf("room %s closed", r.code)
		return
	}
	r.broadcastJSON(nil, Message{T: "peer_left", ID: c.id})
	log.Printf("room %s: player %d left", r.code, c.id)
}

// start forwards the host's start message to every guest and marks the room
// as running. Guests sending start are ignored.
func (g *Registry) start(c *Client, m Message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := c.room
	if r == nil || r.host != c {
		return
	}
	r.started = true
	m.From = c.id
	r.broadcastJSON(c, m)
}

// forward routes a control message from c to one player or to everyone else.
func (g *Registry) forward(c *Client, m Message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := c.room
	if r == nil {
		return
	}
	m.From = c.id
	if m.To != 0 {
		if other := r.members[m.To]; other != nil {
			other.sendJSON(m)
		}
		return
	}
	r.broadcastJSON(c, m)
}

// route delivers a game frame from c to one player, or to everyone else in
// the room when target is 0. payload is the frame without its routing byte.
func (g *Registry) route(c *Client, target int, payload []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routeLocked(c, target, payload)
}

// routeLocked is route with Registry.mu held. The outgoing frame is the
// payload prefixed with the sender's id.
func (g *Registry) routeLocked(c *Client, target int, payload []byte) {
	r := c.room
	if r == nil {
		return
	}
	frame := make([]byte, 1+len(payload))
	frame[0] = byte(c.id)
	copy(frame[1:], payload)
	if target != 0 {
		if other := r.members[target]; other != nil {
			other.deliver(frame)
		}
		return
	}
	for id, other := range r.members {
		if id != c.id {
			other.deliver(frame)
		}
	}
}

// routeUDP handles one datagram from the UDP socket: it looks the sender up
// by key, remembers the address so replies can go out over UDP, answers
// hellos, and routes anything else like route does.
func (g *Registry) routeUDP(from *net.UDPAddr, key uint64, target int, payload []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.byKey[key]
	if c == nil {
		return
	}
	if c.udp == nil || !c.udp.IP.Equal(from.IP) || c.udp.Port != from.Port {
		c.udp = from
	}
	if len(payload) == 0 {
		_, _ = udpConn.WriteToUDP(udpHelloReply, from)
		return
	}
	g.routeLocked(c, target, payload)
}

// broadcastJSON sends m to every member except skip (nil to send to all).
func (r *Room) broadcastJSON(skip *Client, m Message) {
	for _, other := range r.members {
		if other != skip {
			other.sendJSON(m)
		}
	}
}

// deliver hands a game frame to a client over UDP when its address is known,
// otherwise over the WebSocket.
func (c *Client) deliver(frame []byte) {
	if c.udp != nil {
		_, _ = udpConn.WriteToUDP(frame, c.udp)
		return
	}
	c.queue(websocket.BinaryMessage, frame)
}

func (c *Client) keyHex() string {
	return fmt.Sprintf("%016x", c.key)
}

// newKey returns a random non-zero 63-bit session key.
func newKey() uint64 {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		log.Fatalf("crypto/rand: %v", err)
	}
	return n.Uint64() + 1
}

// newCode returns a random room code from an alphabet without I and O, so
// codes read aloud or typed from a screenshot do not get confused with 1 and 0.
func newCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeLetters))))
		if err != nil {
			log.Fatalf("crypto/rand: %v", err)
		}
		b[i] = codeLetters[n.Int64()]
	}
	return string(b)
}
