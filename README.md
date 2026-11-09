<img width="1774" height="887" alt="render_logo_wide" src="https://github.com/user-attachments/assets/256d2624-5c24-4cd9-8db6-df453253b64f" />
# Roomshift room server

The relay that makes co-op work. It knows nothing about the game: one player hosts a room
and gets a four-letter code, up to three others join with that code, and from then on the
relay forwards whatever the players send each other. The host's game is the authority for
everything in a run, so the relay never needs updating when the game's rules change. It only
needs updating when the wire protocol changes (see "Protocol version").

One binary, one port, no database, no config file. A room lives in memory until its host
disconnects.

## Files

| file | what it holds |
|---|---|
| `main.go` | flags, the HTTP mux, `/health` |
| `protocol.go` | protocol version, limits, the JSON message type, frame layouts |
| `room.go` | rooms, clients, the registry and all routing decisions |
| `ws.go` | the WebSocket side: read/write loops, send queue, control messages |
| `udp.go` | the UDP socket and its read loop |
| `doc.go` | package overview |

## Run it

Needs Go 1.22 or newer.

```bash
cd server
go run .                 # listens on :8765, TCP and UDP
go run . -addr :9000     # another port
```

Build binaries instead:

```bash
go build -o roomshift-server .                                        # for this machine
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o roomshift-server-linux .   # for linux machines
```

Check it is up:

```bash
curl http://localhost:8765/health
```

```json
{"ok":true,"players":0,"rooms":0,"version":3}
```

### Firewall

Open the port for **both TCP and UDP**. TCP carries the WebSocket control channel and is
enough for a room to work; UDP carries the game frames once it gets through. Without UDP the
game falls back to sending everything over the WebSocket, which works but feels worse.

```bash
sudo ufw allow 8765/tcp
sudo ufw allow 8765/udp
```

### Pointing the game at it

Players put `host:port` into Settings → Game → Room server, for example
`94.156.37.124:8765`. The game adds `ws://` itself. `wss://host` also works if you put the
relay behind a TLS-terminating reverse proxy, but then UDP has to reach the relay directly on
its own port, so keep the plain port open too.

## Deploy to the VPS

The live relay runs on `94.156.37.124:8765` as the systemd service `roomshift-relay`, from
`/home/tempadmin/roomshift/roomshift-server`. Everything below runs from Git Bash on the dev
machine.

```bash
bash tools/deploy_vps.sh
```

That builds the Linux binary, copies it over SSH with the key in `~/.ssh/roomshift_vps`,
restarts the service and prints the health check from outside. The restart needs root, which
`tools/vps_sudo.ps1` handles with a password stored encrypted with DPAPI; the password never
appears in a terminal or a log. Environment overrides: `VPS_HOST`, `VPS_USER`, `ROOM_PORT`.

First-time setup on a fresh machine:

1. `powershell -File tools/vps_set_password.ps1` stores the sudo password (DPAPI, this user only).
2. `powershell -File tools/authorize_vps_key.ps1` installs the SSH key.
3. Copy `tools/roomshift-relay.service` and `tools/install_relay_service.sh` to the VPS home,
   then run the installer through sudo:
   `powershell -File tools/vps_sudo.ps1 "bash ~/install_relay_service.sh"`.
4. Open the port: `powershell -File tools/vps_sudo.ps1 "ufw allow 8765/tcp && ufw allow 8765/udp"`.

Useful afterwards:

```bash
powershell -File tools/vps_sudo.ps1 "systemctl status roomshift-relay"
powershell -File tools/vps_sudo.ps1 "journalctl -u roomshift-relay -n 50 --no-pager"
```

The unit file in `tools/roomshift-relay.service` is the one installed there. `Restart=always`
brings the relay back within two seconds if it ever dies.

## Docker

```bash
docker build -t roomshift-server .
docker run -d --restart unless-stopped -p 8765:8765/tcp -p 8765:8765/udp roomshift-server
```

## Protocol version

`ProtocolVersion` in `protocol.go` must equal `Net.PROTOCOL` in `scripts/net.gd`. A game
with a different version is refused at host/join with a message that says which side is
out of date. Bump both together whenever a frame layout or a control message changes in a
way old clients cannot handle, then deploy the relay before shipping the game build.

## Protocol

Everything a player sends arrives on one of two channels, both on the same port.

### Control channel: WebSocket text frames, JSON

From a player:

| message | meaning |
|---|---|
| `{"t":"host","v":3}` | create a room → `hosted` |
| `{"t":"join","code":"ABCD","v":3}` | join a room → `joined`; the others get `peer_joined` |
| `{"t":"start","d":{...}}` | host only; forwarded to every guest as `start` |
| `{"t":"msg","to":0,"d":{...}}` | forwarded as `msg`; `to` is a player id, 0 means everyone else |
| `{"t":"leave"}` | leave the room; closing the socket does the same |
| `{"t":"ping"}` | answered with `{"t":"pong"}` |

From the relay:

| message | meaning |
|---|---|
| `{"t":"hosted","code":"ABCD","id":1,"key":"…","uport":8765}` | you are player 1 of a new room |
| `{"t":"joined","code":"ABCD","id":n,"peers":[…],"host":1,"key":"…","uport":8765}` | you are player n |
| `{"t":"peer_joined","id":n}` / `{"t":"peer_left","id":n}` | room membership changed |
| `{"t":"closed","why":"host left"}` | the room is gone |
| `{"t":"start","from":1,"d":{…}}` / `{"t":"msg","from":n,"d":{…}}` | forwarded traffic |
| `{"t":"error","why":"…","v":3}` | refused: bad json, no such room, room full, version mismatch |

Player ids: the host is always 1, guests get 2, 3, 4 in join order. Ids are never reused
inside a room. `key` is a 16-hex-digit session key and `uport` the UDP port, both used below.

The game sends most of its own control traffic (chat, ready state, revives, card picks)
as `msg` only until UDP is confirmed; after that it runs its own reliable ordered stream on
top of UDP game frames, which the relay does not need to understand.

### Game frames

A game frame is opaque to the relay apart from its first byte, the kind. On the wire it is
prefixed with one routing byte.

Over the WebSocket, as a binary frame:

```
in:   [target id][frame]      target 0 = everyone else in the room
out:  [sender id][frame]
```

Over UDP, as a datagram:

```
in:   [8-byte session key][target id][frame]
out:  [sender id][frame]
```

A datagram with an empty frame is a hello. The relay remembers the sender's address and
answers `{0, 0x55}`. The game sends a hello every half second until it gets that answer,
then every five seconds to keep the NAT mapping alive, and only switches its game frames to
UDP once the first answer arrives. Datagrams with an unknown key are dropped without a
reply.

Frames addressed to a player are sent over UDP when the relay knows that player's address,
otherwise over their WebSocket. Frame kind 1 (a world snapshot) is dropped when a player's
WebSocket queue is full, because the next one supersedes it; any other frame that does not
fit closes that player's connection rather than stalling everyone else.

## Limits and behaviour

- Four players per room, enforced here and in the game.
- Room codes use 24 letters (no I or O), 331,776 combinations, picked fresh until unused.
- A client that does not answer a WebSocket ping for 40 seconds is dropped and leaves its
  room. When the host leaves, every guest gets `closed` and the room is deleted.
- Frames over 256 KB close the connection.
- Logs go to stdout: one line per room hosted, player joined, player left, room closed, and
  one when a connection is dropped for a full queue. Nothing about frame contents is logged.
