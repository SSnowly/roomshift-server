package main

import "encoding/json"

// ProtocolVersion must match the game's Net.PROTOCOL. A host or join request
// with a different version is refused with a human-readable error so players
// learn whether the game or the server is the old one.
const ProtocolVersion = 3

// Room limits. The game enforces the same player cap on its side; the relay
// enforces it here so a modified client cannot overfill a room.
const (
	MaxPlayers  = 4
	codeLength  = 4
	codeLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
)

// Game frame kinds the relay needs to recognise. Every other kind is opaque.
//
// A game frame is the payload the players exchange. On the wire it is always
// prefixed by one routing byte: the target id on the way in (0 = everyone
// else in the room) and the sender id on the way out. The first payload byte
// is the frame kind.
const (
	kindSnapshot byte = 1 // host -> guest world state; newer ones supersede older ones, so it may be dropped under pressure
)

// UDP datagram layout from a player to the relay:
//
//	[0:8]  session key, big-endian, handed out in the hosted/joined message
//	[8]    target id (0 = everyone else in the room)
//	[9:]   game frame; empty means "hello, learn my address"
//
// The relay answers a hello with the two bytes {0, 0x55}. A player keeps
// sending hellos every few seconds so NAT mappings stay open; game frames are
// not switched to UDP until the first answer arrives.
const (
	udpKeyLen    = 8
	udpHeaderLen = udpKeyLen + 1
)

var udpHelloReply = []byte{0, 0x55}

// Message is the JSON envelope used on the control channel in both directions.
// Only the fields relevant to a given message type are set.
//
// From a player:
//
//	{"t":"host","v":3}                      create a room
//	{"t":"join","code":"ABCD","v":3}        join a room
//	{"t":"leave"}                           leave the room
//	{"t":"start","d":{...}}                 host only: forwarded to every guest
//	{"t":"msg","to":0,"d":{...}}            forwarded to one player, or everyone else when to is 0
//	{"t":"ping"}                            answered with pong
//
// From the relay:
//
//	{"t":"hosted","code":"ABCD","id":1,"key":"<16 hex>","uport":8765}
//	{"t":"joined","code":"ABCD","id":n,"peers":[...],"host":1,"key":"<16 hex>","uport":8765}
//	{"t":"peer_joined","id":n}
//	{"t":"peer_left","id":n}
//	{"t":"closed","why":"host left"}
//	{"t":"start","from":1,"d":{...}}
//	{"t":"msg","from":n,"d":{...}}
//	{"t":"pong"}
//	{"t":"error","why":"...","v":3}
type Message struct {
	T     string          `json:"t"`
	Code  string          `json:"code,omitempty"`
	To    int             `json:"to,omitempty"`
	From  int             `json:"from,omitempty"`
	ID    int             `json:"id,omitempty"`
	Peers []int           `json:"peers,omitempty"`
	Host  int             `json:"host,omitempty"`
	D     json.RawMessage `json:"d,omitempty"`
	Why   string          `json:"why,omitempty"`
	V     int             `json:"v,omitempty"`
	Key   string          `json:"key,omitempty"`
	UPort int             `json:"uport,omitempty"`
}

func errorMessage(why string) Message {
	return Message{T: "error", Why: why, V: ProtocolVersion}
}
