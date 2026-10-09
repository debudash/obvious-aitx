# MCPTT iOS field client

Native Swift/SwiftUI mirror of the Android field-client contract (spec: pinned
MCPTT blueprint, "Android and iOS field clients"). One functional contract
across both platforms: login, roster with affiliations, group and private
calls, one-button PTT with live floor state, guarded emergency trigger,
reconnect with backoff, alert retry until acknowledged.

## Layout

    Package.swift            SwiftPM manifest — MCPTTCore library + tests
    Sources/MCPTTCore/       platform-independent core (builds and tests on
                             Linux CI — no Apple-only imports)
      Protocol/              wire frames mirroring server/internal/protocol
                             (MessageCodec, SignalingTypes, WireModels, GoTime)
      State/                 FloorReducer (client floor machine), CallStore,
                             RosterStore — pure reducers, unit-tested
      Resilience/            ReconnectBackoff + AlertRetryQueue — pure, tested
      Audio/                 AudioSessionPlan — the AVAudioSession policy as
                             testable string values
      Media/                 MediaPlan — direction/mic gating per floor state
    App/Sources/             SwiftUI app + transports (Apple platforms only;
                             built by the Xcode project, NOT by SwiftPM CI)
      MCPTTApp / AppModel    app shell, login/logout, tab root
      LoginScreen            server URL + credentials
      TalkScreen             affiliated groups, active call, ringing sheet,
                             denied toast, pre-emption banner
      PTTButton              the floor indicator: grant / queue position /
                             denied / pre-empted, burst countdown, 1.5 s
                             emergency hold arming the confirmation sheet
      SessionController      orchestrator: signaling loop, store pumps,
                             effect execution (frames, mic gate, burst timer)
      SignalingClient        URLSessionWebSocketTask + MessageCodec
      APIClient              the seven REST routes the client uses
      MediaClient            WebRTC leg (stasel/WebRTC framework): client
                             offer with room token, mic gated by the reducer
      AudioSessionController play-and-record / voiceChat application
      LocationProvider       client-reported WGS-84 fix for emergencies
      EmergencyController    guarded gesture → alert (+ optional call) with
                             the durable retry queue
    App/project.yml          XcodeGen spec (generates MCPTT.xcodeproj)
    Tests/MCPTTCoreTests/    92 XCTests: codec, floor table, call/roster
                             reducers, backoff, alert retry, Go time,
                             audio/media policy

## Testing on Linux (this repo's CI)

    cd mcptt/ios
    swift build && swift test

`MCPTTCore` has no external dependencies; any Swift 6.0.3 toolchain builds
it. The app layer requires Xcode (see the known constraint below).

## Known constraint: no macOS toolchain in this sandbox

The build environment is Linux. `swift build`/`swift test` cover MCPTTCore
fully; the SwiftUI/WebRTC app layer cannot be compiled or assembled into an
IPA here. **Documented follow-up: run `xcodegen generate` in `App/` and build
the `MCPTT` scheme in Xcode 15+ (iOS 17 deployment target) on a macOS
machine.** The app sources are written to compile under that target; the
compile-source guarantee is enforced by review, not by a toolchain run.

## Server contract gaps the client tolerates (flagged, not worked around)

1. **WSS client-frame ingest** — the server's WebSocket read pump discards
   inbound frames, so `FloorRequest`/`FloorReleased`/`CallStart` are sent per
   contract but have no server effect yet. The client keeps the reducer fully
   server-shaped: state changes only on server frames, never optimistically
   beyond the reducer's own `requesting` state.
2. **Private-call callee field** — `CallStart` carries no callee on the wire,
   so a private call's target is not expressible yet. The client decodes an
   optional `calleeId` (ringing sheet appears when it names this user) and
   otherwise surfaces the call without a ring target.
3. **Room-token delivery** — no wire frame delivers the SFU's room-scoped
   media token to a client, so `MediaClient.join` defers the media leg until
   `roomToken` is populated. No client change will be needed when delivery
   lands: the property is the only seam.

The REST surface (login, users/groups/affiliations bootstrap, emergency call,
voiceless alert + list) is fully live and is what the screens render.

## Wire conformance

`Tests/MCPTTCoreTests/MessageCodecTests.swift` pins every frame against the
server's Go structs (`server/internal/protocol/protocol.go`) — field names,
JSON keys, omitempty tolerances (e.g. `FloorDenied` without `reason`), and
unknown-type forward compatibility. `WireModelsTests` pins the REST DTOs the
same way. A server-side drift fails here, loudly, before it can mislead a
user.
