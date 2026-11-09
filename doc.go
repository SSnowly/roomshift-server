// Command roomshift-server is the room relay for Roomshift co-op.
//
// It knows nothing about the game. One player hosts a room and receives a
// four-letter code; up to three others join with that code; from then on the
// relay forwards whatever the players send each other. The host's game is the
// authority for everything that happens in a run.
//
// Two channels run between each player and the relay, both on the same port:
//
//   - A WebSocket (TCP) carries the control channel: JSON text frames for
//     hosting, joining, leaving and starting, plus a fallback path for game
//     frames when UDP does not get through.
//   - A UDP socket carries game frames once a player has proven they own their
//     session key. This is the fast path for snapshots and inputs.
//
// See protocol.go for the exact message and frame layouts, and README.md for
// how to run and deploy the binary.
package main
