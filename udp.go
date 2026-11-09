package main

import (
	"encoding/binary"
	"errors"
	"log"
	"net"
)

// udpConn is the single UDP socket, bound to the same port as the WebSocket
// listener. udpPort is told to players in hosted/joined.
var (
	udpConn *net.UDPConn
	udpPort int
)

// serveUDP reads datagrams forever. Datagrams that are too short or carry an
// unknown key are dropped silently; that is also what happens to stale
// traffic from a player who already left.
func serveUDP() {
	buf := make([]byte, 65536)
	for {
		n, from, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n < udpHeaderLen {
			continue
		}
		key := binary.BigEndian.Uint64(buf[:udpKeyLen])
		target := int(buf[udpKeyLen])
		reg.routeUDP(from, key, target, buf[udpHeaderLen:n])
	}
}

// listenUDP binds the UDP socket on addr and starts serveUDP.
func listenUDP(addr string) error {
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	udpConn, err = net.ListenUDP("udp", uaddr)
	if err != nil {
		return err
	}
	udpPort = udpConn.LocalAddr().(*net.UDPAddr).Port
	go serveUDP()
	log.Printf("udp listening on %s", udpConn.LocalAddr())
	return nil
}
