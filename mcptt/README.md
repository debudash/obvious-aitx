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
| `server/`   | Go module: HTTP/WSS control plane, SQLite store, floor contract |
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

Go definitions: `server/internal/protocol` (wire types) and
`server/internal/floor` (priority ladder + `FloorController` interface).

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
