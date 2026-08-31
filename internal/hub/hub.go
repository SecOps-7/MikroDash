package hub

// Rooms, and the fan-out that uses them.
//
// This is the piece PLAN.md B3 insists must be explicit in Go rather than
// inherited from a library: "Rooms and their authorization role are
// reimplemented explicitly in Go, since room membership is the enforcement
// point, not an optimisation." A socket receives a router's telemetry because
// it is in that router's room, and it is in that room because a permission
// check let it join. Losing that distinction turns a gate into a filter.
//
// Room names match the Node scheme exactly, because the rooms ARE the wire
// contract: buildRouterIo in src/index.js turns a collector's `io.to('page-dns')`
// into `router-<rid>-page-dns`, and this does the same.
//
// BACKPRESSURE IS HANDLED DIFFERENTLY FROM SOCKET.IO, DELIBERATELY. Socket.IO
// buffers without bound for a client that has stopped reading, which on a
// dashboard pushing every second is a memory leak wearing a slow phone as a
// disguise. Here a client that cannot keep up loses FRAMES, not its connection:
// the send is non-blocking and a full queue drops the message. That is safe
// precisely because of how the collectors behave — every payload is a complete
// snapshot rather than a delta, a change re-emits, and opening a page replays
// the last payload — so a dropped frame self-heals on the next tick instead of
// leaving the page permanently wrong. Dropping the connection instead would
// turn a brief stall into a visible reconnect.

import (
	"encoding/json"
	"log"
	"sync"
)

// Envelope is the wire format: one named event and its payload, which is the
// whole of what the app used Socket.IO for.
type Envelope struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Client is one browser connection. The transport lives in package server; this
// side only needs somewhere to put bytes.
type Client struct {
	// Send is drained by the connection's writer goroutine.
	Send chan []byte
	// ID is for logging only.
	ID string

	mu      sync.Mutex
	rooms   map[string]bool
	dropped int
}

func NewClient(id string, queue int) *Client {
	return &Client{Send: make(chan []byte, queue), ID: id, rooms: map[string]bool{}}
}

// Rooms is a snapshot, for teardown and for the leave-everything path a
// revocation takes.
func (c *Client) Rooms() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.rooms))
	for r := range c.rooms {
		out = append(out, r)
	}
	return out
}

// Dropped counts frames this client was too slow to receive.
func (c *Client) Dropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

func (c *Client) deliver(b []byte) {
	select {
	case c.Send <- b:
	default:
		c.mu.Lock()
		c.dropped++
		n := c.dropped
		c.mu.Unlock()
		// Once, then silence: a client that is stalled drops every frame, and
		// logging each one turns one slow phone into a flooded log.
		if n == 1 {
			log.Printf("[hub] %s is not keeping up; dropping frames", c.ID)
		}
	}
}

// Hub owns room membership.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]bool
	rooms   map[string]map[*Client]bool
}

func New() *Hub {
	return &Hub{clients: map[*Client]bool{}, rooms: map[string]map[*Client]bool{}}
}

func (h *Hub) Add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
}

// Remove takes a client out of every room and out of the hub. Safe to call more
// than once, because a connection can end from either the read or the write
// side and both call it.
func (h *Hub) Remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return
	}
	delete(h.clients, c)
	c.mu.Lock()
	for r := range c.rooms {
		h.dropFromRoom(r, c)
	}
	c.rooms = map[string]bool{}
	c.mu.Unlock()
	close(c.Send)
}

// dropFromRoom must be called with h.mu held.
func (h *Hub) dropFromRoom(room string, c *Client) {
	if m := h.rooms[room]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.rooms, room)
		}
	}
}

func (h *Hub) Join(c *Client, room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return
	}
	if h.rooms[room] == nil {
		h.rooms[room] = map[*Client]bool{}
	}
	h.rooms[room][c] = true
	c.mu.Lock()
	c.rooms[room] = true
	c.mu.Unlock()
}

func (h *Hub) Leave(c *Client, room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropFromRoom(room, c)
	c.mu.Lock()
	delete(c.rooms, room)
	c.mu.Unlock()
}

// Occupants is how many clients are in a room. The collector supervisor reads
// it to decide whether anybody is still watching — the finer gate that stops a
// collector polling for a page nobody has open.
func (h *Hub) Occupants(room string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[room])
}

// Broadcast sends one event to everybody in a room.
func (h *Hub) Broadcast(room, event string, payload any) {
	h.mu.RLock()
	members := make([]*Client, 0, len(h.rooms[room]))
	for c := range h.rooms[room] {
		members = append(members, c)
	}
	h.mu.RUnlock()
	if len(members) == 0 {
		return // nobody watching; do not pay for the marshal
	}
	b, err := json.Marshal(Envelope{Event: event, Data: payload})
	if err != nil {
		log.Printf("[hub] cannot marshal %s: %v", event, err)
		return
	}
	for _, c := range members {
		c.deliver(b)
	}
}

// BroadcastAll sends one event to EVERY connected client, in no room at all.
//
// This is socket.io's `io.emit`, and it has exactly two callers in the live app,
// both in `POST /api/settings`: which pages a browser may draw is a fleet-wide
// fact, not a per-router one, so a viewer looking at any router — or at none —
// has to hear about it.
//
// ROOMS ARE THE DEFAULT AND THIS IS THE EXCEPTION. Everything else in this port
// is addressed to a room, because almost every payload is about one router and
// broadcasting it fleet-wide would tell a viewer about hardware they may not be
// permitted to see. Reach for `Broadcast` unless the fact really is global.
func (h *Hub) BroadcastAll(event string, payload any) {
	h.mu.RLock()
	members := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		members = append(members, c)
	}
	h.mu.RUnlock()
	if len(members) == 0 {
		return // nobody connected; do not pay for the marshal
	}
	b, err := json.Marshal(Envelope{Event: event, Data: payload})
	if err != nil {
		log.Printf("[hub] cannot marshal %s: %v", event, err)
		return
	}
	for _, c := range members {
		c.deliver(b)
	}
}

// BroadcastExcept sends one event to everybody in a room APART FROM one client.
//
// It exists for a specific shape: an action whose result is per-viewer. The
// Backups page's payload carries `permitted`, computed for the socket that asked
// — so broadcasting it after a delete would tell a viewer they may write.
// Instead the actor gets their own payload and everybody else is NUDGED to
// re-request theirs, which is what socket.io's `socket.to(room).emit()` does and
// what the live app relies on.
func (h *Hub) BroadcastExcept(room string, except *Client, event string, payload any) {
	h.mu.RLock()
	members := make([]*Client, 0, len(h.rooms[room]))
	for c := range h.rooms[room] {
		if c != except {
			members = append(members, c)
		}
	}
	h.mu.RUnlock()
	if len(members) == 0 {
		return // nobody else watching; do not pay for the marshal
	}
	b, err := json.Marshal(Envelope{Event: event, Data: payload})
	if err != nil {
		log.Printf("[hub] cannot marshal %s: %v", event, err)
		return
	}
	for _, c := range members {
		c.deliver(b)
	}
}

// BroadcastRooms sends one event to the UNION of several rooms, once per
// client.
//
// It exists because socket.io's `io.to(a).to(b).emit()` delivers a single copy
// to a client in both rooms, and interfaceStatus relies on that: its full
// payload goes to three rooms at once, and a viewer with the Interfaces page
// open inside the dashboard is in two of them. Looping Broadcast would send
// that client the same frame twice.
func (h *Hub) BroadcastRooms(rooms []string, event string, payload any) {
	h.mu.RLock()
	seen := map[*Client]bool{}
	members := make([]*Client, 0)
	for _, room := range rooms {
		for c := range h.rooms[room] {
			if !seen[c] {
				seen[c] = true
				members = append(members, c)
			}
		}
	}
	h.mu.RUnlock()
	if len(members) == 0 {
		return // nobody watching; do not pay for the marshal
	}
	b, err := json.Marshal(Envelope{Event: event, Data: payload})
	if err != nil {
		log.Printf("[hub] cannot marshal %s: %v", event, err)
		return
	}
	for _, c := range members {
		c.deliver(b)
	}
}

// Send delivers one event to one client — the reply to something it asked for,
// and the replay of a last payload when it opens a page.
func (h *Hub) Send(c *Client, event string, payload any) {
	b, err := json.Marshal(Envelope{Event: event, Data: payload})
	if err != nil {
		log.Printf("[hub] cannot marshal %s: %v", event, err)
		return
	}
	c.deliver(b)
}
