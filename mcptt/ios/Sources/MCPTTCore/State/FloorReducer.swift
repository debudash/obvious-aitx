// The client's mirror of the server's floor machine. The server is the sole
// arbiter — this reducer never grants anything by itself; it renders the
// wire's decisions into the PTT button's state and emits the effects the
// session controller executes (send frames, gate the mic, run the burst
// timer). Kept pure and synchronous so the whole state table is unit-tested
// (spec acceptance: "table-driven state machine tests").
//
// State semantics per the spec's floor table:
//   Idle        — PTT ready
//   Requesting  — burst request in flight
//   Granted     — transmitting, burst countdown running (server max 60 s)
//   Queued      — waiting at position N; releasing cancels the entry
//   Denied      — reason toast; idle after acknowledgement; re-request allowed
//   Preempted   — audio cut, "pre-empted by …" banner; holding PTT rejoins
//                 the queue, releasing returns to idle

import Foundation

/// The user's own floor state within one call. Other parties' floor state
/// lives in CallStore (speaker/queue display), not here.
public enum FloorState: Equatable, Sendable {
    case idle
    case requesting
    case granted(burstStartedAt: TimeInterval)
    case queued(position: Int)
    case denied(reason: String)
    case preempted(by: String, emergency: Bool)
}

/// Inputs to the reducer. Wire events arrive from SignalingTransport; PTT and
/// timer events from the UI layer. `pttHeld` rides the pre-emption event
/// because the reducer cannot see the button — the session controller
/// composes it from view state.
public enum FloorEvent: Equatable, Sendable {
    case pttPressed
    case pttReleased
    /// FloorGranted for this user; `at` is the burst start (seconds, any
    /// monotonic or wall clock — only differences are used).
    case granted(at: TimeInterval)
    case denied(reason: String, queuePosition: Int)
    case preempted(by: String, emergency: Bool, pttHeld: Bool)
    /// The countdown reached zero — mirroring the server's max-duration
    /// expiry before its own release fan-out arrives.
    case burstExpired
    case denyAcknowledged
    case callEnded
}

/// Outbound commands the session controller executes. Reducers produce them;
/// nothing in the core performs I/O.
public enum FloorEffect: Equatable, Sendable {
    case sendFloorRequest
    case sendFloorRelease
    case startTransmitting
    case stopTransmitting
    case startBurstTimer(startedAt: TimeInterval, duration: TimeInterval)
    case cancelBurstTimer
    case showDeniedToast(reason: String)
    case dismissDeniedToast
    case showPreemptBanner(by: String, emergency: Bool)
    case dismissPreemptBanner
}

public struct FloorTransition: Equatable, Sendable {
    public let state: FloorState
    public let effects: [FloorEffect]

    public init(_ state: FloorState, _ effects: [FloorEffect]) {
        self.state = state
        self.effects = effects
    }

    public static func same(_ state: FloorState) -> FloorTransition {
        FloorTransition(state, [])
    }
}

public enum FloorReducer {
    /// The server's floor.DefaultMaxTalkDuration; configurable per group
    /// server-side. The server exempts emergency bursts from expiry — it
    /// remains the sole authority; the client's timer only drives display.
    public static let defaultMaxTalkDuration: TimeInterval = 60

    public static func reduce(
        _ state: FloorState,
        _ event: FloorEvent,
        maxTalkDuration: TimeInterval = FloorReducer.defaultMaxTalkDuration
    ) -> FloorTransition {
        switch (state, event) {

        // MARK: Idle

        case (.idle, .pttPressed):
            return FloorTransition(.requesting, [.sendFloorRequest])
        case (.idle, .callEnded):
            return .same(.idle)

        // MARK: Requesting

        case (.requesting, .granted(let at)):
            return grantedTransition(at: at, maxTalkDuration: maxTalkDuration)
        case (.requesting, .denied(let reason, _)):
            return FloorTransition(.denied(reason: reason), [
                .showDeniedToast(reason: reason),
                .dismissPreemptBanner,
            ])
        case (.requesting, .pttReleased):
            // Cancel the in-flight request / queued entry.
            return FloorTransition(.idle, [.sendFloorRelease])
        case (.requesting, .preempted(let by, let emergency, let pttHeld)):
            // Not the holder yet, so a pre-empt naming us is a contract
            // surprise; render the banner and re-request if still held.
            return preemptedTransition(by: by, emergency: emergency, pttHeld: pttHeld)

        // MARK: Granted

        case (.granted, .pttReleased):
            return releaseTransition()
        case (.granted, .burstExpired):
            // Mirror the server's expiry: stop capture, tell the server.
            return releaseTransition()
        case (.granted, .granted(let at)):
            // Re-grant echo (direct-call restatement): re-arm the timer.
            return FloorTransition(.granted(burstStartedAt: at), [
                .startTransmitting,
                .startBurstTimer(startedAt: at, duration: maxTalkDuration),
                .dismissDeniedToast,
                .dismissPreemptBanner,
            ])
        case (.granted, .denied(let reason, _)):
            // Dispatch revoke arrives as a denial naming us while we hold
            // the floor (the server's Revoke emits a denied decision for the
            // stripped holder).
            return FloorTransition(.denied(reason: reason), [
                .stopTransmitting,
                .cancelBurstTimer,
                .showDeniedToast(reason: reason),
            ])
        case (.granted, .preempted(let by, let emergency, let pttHeld)):
            var effects: [FloorEffect] = [
                .stopTransmitting,
                .cancelBurstTimer,
                .showPreemptBanner(by: by, emergency: emergency),
            ]
            if pttHeld {
                // Spec: "original speaker joins queue if still holding PTT".
                effects.append(.sendFloorRequest)
                return FloorTransition(.requesting, effects)
            }
            return FloorTransition(.preempted(by: by, emergency: emergency), effects)

        // MARK: Queued

        case (.queued, .granted(let at)):
            return grantedTransition(at: at, maxTalkDuration: maxTalkDuration)
        case (.queued, .denied(let reason, _)):
            // Queue full or listen-only rejection.
            return FloorTransition(.denied(reason: reason), [.showDeniedToast(reason: reason)])
        case (.queued, .pttReleased):
            return FloorTransition(.idle, [.sendFloorRelease])
        case (.queued, .preempted(let by, let emergency, let pttHeld)):
            return preemptedTransition(by: by, emergency: emergency, pttHeld: pttHeld)

        // MARK: Denied

        case (.denied, .pttPressed):
            // "Idle after acknowledgement; may re-request" — a fresh press
            // is an acknowledgement and a new request in one gesture.
            return FloorTransition(.requesting, [
                .sendFloorRequest,
                .dismissDeniedToast,
            ])
        case (.denied, .denyAcknowledged):
            return FloorTransition(.idle, [.dismissDeniedToast])
        case (.denied, .granted(let at)):
            // Stale denial racing a late grant: the grant wins.
            return grantedTransition(at: at, maxTalkDuration: maxTalkDuration)
        case (.denied, .callEnded):
            return FloorTransition(.idle, [.dismissDeniedToast])

        // MARK: Preempted

        case (.preempted, .pttPressed):
            return FloorTransition(.requesting, [
                .sendFloorRequest,
                .dismissPreemptBanner,
            ])
        case (.preempted, .pttReleased):
            return FloorTransition(.idle, [.dismissPreemptBanner])
        case (.preempted, .preempted(let by, let emergency, _)):
            // A second pre-empt (emergency stripped by net control): update
            // the banner to name the new holder.
            return FloorTransition(.preempted(by: by, emergency: emergency), [
                .showPreemptBanner(by: by, emergency: emergency),
            ])
        case (.preempted, .granted(let at)):
            // Our queued re-request reached the head.
            return grantedTransition(at: at, maxTalkDuration: maxTalkDuration)
        case (.preempted, .denied(let reason, _)):
            // The re-request was refused (e.g. net control still holds).
            return FloorTransition(.denied(reason: reason), [
                .showDeniedToast(reason: reason),
                .dismissPreemptBanner,
            ])

        // MARK: Call end — from every state

        case (_, .callEnded):
            return FloorTransition(.idle, [
                .stopTransmitting,
                .cancelBurstTimer,
                .dismissDeniedToast,
                .dismissPreemptBanner,
            ])

        // MARK: No-ops (spurious or already-handled inputs)

        case (.granted, .pttPressed),
             (.queued, .pttPressed),
             (.requesting, .pttPressed),
             (.preempted, .denyAcknowledged),
             (.granted, .denyAcknowledged),
             (.requesting, .denyAcknowledged),
             (.queued, .denyAcknowledged),
             (.idle, .pttReleased),
             (.idle, .granted),
             (.idle, .denied),
             (.idle, .denyAcknowledged),
             (.idle, .burstExpired),
             (.idle, .preempted),
             (.requesting, .burstExpired),
             (.queued, .burstExpired),
             (.denied, .pttReleased),
             (.denied, .burstExpired),
             (.denied, .denied),
             (.denied, .preempted),
             (.preempted, .burstExpired):
            return .same(state)
        }
    }

    // MARK: Shared transition builders

    private static func grantedTransition(at: TimeInterval, maxTalkDuration: TimeInterval) -> FloorTransition {
        FloorTransition(.granted(burstStartedAt: at), [
            .startTransmitting,
            .startBurstTimer(startedAt: at, duration: maxTalkDuration),
            .dismissDeniedToast,
            .dismissPreemptBanner,
        ])
    }

    private static func releaseTransition() -> FloorTransition {
        FloorTransition(.idle, [
            .stopTransmitting,
            .sendFloorRelease,
            .cancelBurstTimer,
        ])
    }

    private static func preemptedTransition(by: String, emergency: Bool, pttHeld: Bool) -> FloorTransition {
        var effects: [FloorEffect] = [.showPreemptBanner(by: by, emergency: emergency)]
        if pttHeld {
            effects.append(.sendFloorRequest)
            return FloorTransition(.requesting, effects)
        }
        return FloorTransition(.preempted(by: by, emergency: emergency), effects)
    }
}
