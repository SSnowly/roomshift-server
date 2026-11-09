package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8765", "address to listen on, for both the WebSocket (TCP) and the game frames (UDP)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveWS)
	mux.HandleFunc("/health", serveHealth)

	if err := listenUDP(*addr); err != nil {
		log.Fatalf("udp: %v", err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("roomshift room server v%d listening on %s (tcp+udp)", ProtocolVersion, *addr)
	log.Fatal(srv.ListenAndServe())
}

// serveHealth answers GET /health with a small JSON status, for uptime checks
// and for the deploy script to confirm a restart worked.
func serveHealth(w http.ResponseWriter, _ *http.Request) {
	rooms, players := reg.counts()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"rooms":   rooms,
		"players": players,
		"version": ProtocolVersion,
	})
}
