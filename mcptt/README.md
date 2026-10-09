# MCPTT — Mission-Critical Push-to-Talk

A 3GPP Release 18-aligned mission-critical push-to-talk system: one Go server
(control plane + media plane), a React dispatcher console, and native Android
(Kotlin) and iOS (Swift) field clients. Group, private, and broadcast calls
with server-authoritative floor control — exactly one speaker per call,
arbitrated by priority and pre-emption.

The product spec (scope table, floor state machine, emergency behavior,
acceptance criteria) is the source of truth for what the system must do; this
README is the developer orientation.

## Layout

| Path        | Contents                                                        |
| ----------- | --------------------------------------------------------------- |
| `server/`   | Go module: HTTP/WSS control plane, SQLite store, floor contract, media plane (Pion SFU) |
| `web/`      | React dispatcher console (reserved — console PR)                |
| `android/`  | Kotlin field client (reserved — mobile PR)                      |
| `ios/`      | Swift field client (reserved — mobile PR)                       |

## Architecture

One Go process hosts both planes, so floor decisions and media routing share
memory and cannot disagree:

- **Control plane** — REST + WebSocket (WSS) service: JWT authentication with
  role + priority claims, group/membership CRUD, affiliation service,
  presence, emergency services, audit log. Every protected route rejects
  unauthenticated (401) or wrong-role (403) access; deny paths are tested.
- **Media plane** — Pion WebRTC selective forwarding unit (added in the media
  PR). The SFU forwards audio only for callers the floor controller has
  granted; it is never the authority.

**Boundary invariant:** the floor controller is the single writer of talk
permission. No client, dispatcher, or SFU path may transmit a talk burst
without a floor grant, and no component forwards media for a call the
controller does not consider active.

**Priority ladder** (per-user, configurable): 1–3 ambient, 4–6 standard
field users, 7–8 supervisors, 9 emergency, 10 dispatcher net control.
Higher levels pre-empt active talkers; equal levels queue.

## Protocol contract (WSS JSON)

Signaling is WebSocket JSON with 3GPP TS 24.379-aligned semantics — a
documented deviation from the SIP wire protocol. Message types fixed by the
foundation PR and consumed by every later client:

| Type                | Direction        | Meaning                                        |
| ------------------- | ---------------- | ---------------------------------------------- |
| `FloorRequest`      | client → server  | ask for the talk floor (priority, emergency)   |
| `FloorGranted`      | server → all     | arbitration result with the waiting queue      |
| `FloorDenied`       | server → client  | rejected, with reason and queue position       |
| `FloorReleased`     | client → server  | PTT released; queue head is auto-granted       |
| `FloorPreempted`    | server → all     | a higher-priority floor took the call mid-burst|
| `FloorRevoke`       | dispatcher → srv | strip the current talker's floor               |
| `CallStart`         | client → server  | open a group / private / broadcast call        |
| `CallJoined`        | server → all     | a party joined (late entry)                    |
| `CallEnded`         | server → all     | call torn down                                 |
| `EmergencyAlert`    | client → server  | one-tap alert with client-reported location    |
| `PresenceUpdate`    | server → all     | a user connected to or dropped from `/ws`      |
| `AffiliationChanged`| server → all     | a user's group affiliation changed             |
| `MediaOffer`        | client → server  | WebRTC offer (`callId`, room `token`, `sdp` — exactly one sendrecv audio m-line: the client's microphone) |
| `MediaAnswer`       | server → client  | SFU answer SDP binding the floor-audio track to that m-line, or empty `sdp` + `err` on rejection |

Go definitions: `server/internal/protocol` (wire types),
`server/internal/floor` (priority ladder + `FloorController` interface), and
`server/internal/media` (SFU).

### Media plane

`server/internal/media` hosts the Pion SFU. One room per call, one transport
per participant, Opus 48 kHz as the single negotiated codec, DTLS-SRTP per
hop (the spec's documented security deviation). Signaling is offer/answer on
the WSS channel (`MediaOffer`/`MediaAnswer` above); the room-scoped token
binding caller and call is minted per call and verified before any SDP is
processed — the token issuer is separate from the access-token issuer, so an
access token never admits a media room.

Forwarding is grant-gated: a participant's upstream packets reach the other
listeners only while the **floor gate** — an in-process mirror the control
plane feeds exclusively with `floor.FloorDecision` results — marks that
participant granted. Pre-emption removes the displaced talker's grant in the
same step that grants the winner; a removed participant's transport is
closed and its grant dropped, so it receives zero packets afterwards; a
late joiner attaches to the live room and hears the current speaker
immediately because the SFU forwards live, not a recording.

The gate is currently fed at startup wiring only; the floor-engine PR
replaces that with live controller decisions inside the call-control
critical section (the `Apply`/`RevokeUser`/`Clear` contract is final).

## Carrier RX integration (flag-gated, off by default)

The system is over-the-top and stays that way. Carrier connectivity is a
deployment-time capability: a **receive-only** mirror of what the server has
already arbitrated. It can never grant floor, never accept inbound audio, and
never becomes a second control plane. A future SIP/IWF adapter slots in
behind the same `RXAdapter` interface (`server/integration`) without touching
call control or media.

### Enabling

Configuration is environment-based; every key maps to an `MCPTT_CARRIER_*`
variable and **unknown `MCPTT_CARRIER_*` variables fail the server at
startup** (a misspelled flag must never silently disable the integration):

| Variable | Meaning | Default |
| --- | --- | --- |
| `MCPTT_CARRIER_ENABLED` | Master switch. `false` constructs nothing: no adapter, no socket, no tap. | `false` |
| `MCPTT_CARRIER_ADAPTER` | `rtp` (audio streaming) or `webhook` (JSON events). | `rtp` |
| `MCPTT_CARRIER_ENDPOINT` | Carrier RX `host:port` (RTP adapter). | — |
| `MCPTT_CARRIER_CODEC` | `pcmu` (PT 0, 8 kHz) or `opus` (PT 96, 48 kHz). | `pcmu` |
| `MCPTT_CARRIER_GROUPS` | Comma-separated talkgroup filter; **empty streams nothing**. | empty |
| `MCPTT_CARRIER_EVENTS_URL` | HTTP(S) JSON event webhook target (webhook adapter). | — |

```sh
MCPTT_CARRIER_ENABLED=true \
MCPTT_CARRIER_ADAPTER=rtp \
MCPTT_CARRIER_ENDPOINT=rx.carrier.example:5004 \
MCPTT_CARRIER_GROUPS=tg-fire,tg-ops \
go run ./cmd/server
```

### What the carrier receives

- **Audio (RTP adapter):** only the currently granted talker's bursts —
  the same packets the SFU's floor gate already chose to forward, mirrored
  by a per-call tap — packetized as 20 ms frames to the configured endpoint
  with a stable SSRC and continuous seq/timestamps per stream. While the
  floor is denied, queued, or revoked, the carrier hears **true digital
  silence** (all-0xFF PCMU / empty Opus frames); the cadence never stops.
- **Events (webhook adapter):** `call.started`, `call.ended`, floor
  decisions, and emergency events as JSON with a timestamp and call ID,
  delivered best-effort. Webhooks are a mirror, not a record: durability
  lives in the dispatcher rail and the audit log.
- **Talkgroup filter:** only flagged groups stream at all; unflagged groups
  and private calls never leave the server, flag on or off.
- **Emergency parity:** an emergency upgrade flows through the same grant
  path — the carrier hears whichever talker the floor controller granted,
  exactly like any other grant.

### The flag-off guarantee

With `MCPTT_CARRIER_ENABLED=false` (the default) the integration code path
is never constructed: `NewFromConfig` returns nil, no adapter exists, no
media tap registers, and the server opens **zero external sockets**. This is
asserted by the integration suite (`bridge_test.go`), which fails on any
dial attempt while the flag is off.

## Running

```sh
# server (JWT secret is required, ≥ 32 bytes)
MCPTT_JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/server

# demo roster: 1 dispatcher, 2 supervisors, 6 field users, 4 groups
MCPTT_DB_PATH=demo.db go run ./cmd/seed
# demo password for every seeded account: mcptt-demo-2026
```

REST surface (auth unless noted): `POST /api/auth/login`,
`POST /api/auth/register` (dispatcher), `GET /api/users/me`, `GET /api/users`,
`PATCH /api/users/{id}` (dispatcher), `GET|POST /api/groups`,
`GET|PATCH|DELETE /api/groups/{id}` (mutations dispatcher),
`POST|DELETE /api/groups/{id}/members` (dispatcher),
`POST|DELETE /api/groups/{id}/affiliations` (self if member, dispatcher for
others), `GET /api/affiliations` (self, or any user for dispatchers),
`GET /healthz` (open). `GET /ws?token=<jwt>` is the signaling socket.

## Scope notes

Release 18 on-network core plus the optional capabilities the spec commits to
(dispatcher functions, emergency alerts, late entry, broadcast groups,
functional aliases). ProSe off-network, MBMS bearers, and LMR interworking are
out of scope; MIKEY-SAKKE/KMS end-to-end keys are replaced by hop-by-hop
DTLS-SRTP via WebRTC — all documented deviations in the spec.
