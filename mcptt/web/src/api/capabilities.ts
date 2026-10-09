// Which console capabilities the deployed server build can actually serve.
// The server's call-control engine (SessionManager) supports the full surface,
// but its HTTP/WS layer exposes only the emergency and dispatcher paths —
// client-facing group-call start, private call, floor request/release, audit
// read, and media-token routes are not present yet (verified against main
// 92fed18: router.go exposes none of them; the WS pump drops FloorRequest /
// CallStart inbound frames). Controls behind a `false` flag render disabled
// with an honest reason. Flip these to `true` the moment the server lands the
// routes — the state layer already speaks the full protocol.
export const CAPABILITIES = {
  /** Dispatcher PTT (floor request at P10) — no client floor-request route. */
  floorRequest: false,
  /** Non-emergency group call start — no client route. */
  groupCallStart: false,
  /** Private calls — StartPrivate is unreachable over HTTP/WS. */
  privateCall: false,
  /** Server audit log read — store.ListAudit has no REST route. */
  auditLog: false,
  /** WebRTC media — no route mints room tokens for browser clients. */
  browserMedia: false,
} as const

export const CAPABILITY_NOTE =
  'This server build does not expose the client-facing call-control routes yet (group/private call start, floor request, audit read, media tokens). Emergency calls, broadcasts, and all dispatch controls are live; the console lights the rest up the moment the routes land.'
