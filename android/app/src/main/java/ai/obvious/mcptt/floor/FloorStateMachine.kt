package ai.obvious.mcptt.floor

/**
 * Client-side floor machine — the state the PTT button renders. It mirrors
 * the server's arbitration (mcptt/server/internal/floor) from the wire's
 * point of view: the button IS the floor indicator (spec, mobile-apps
 * section), so every server decision maps to a visible state.
 *
 * The machine is pure: every transition takes an explicit timestamp, all
 * side effects (frames to send, audio to cut) are returned as values, and
 * nothing here touches a socket or a clock. The owning service serializes
 * events (single-threaded handler) and applies the returned effects.
 */
sealed interface FloorState {
    /** PTT ready — nobody talking or last event consumed. */
    data object Idle : FloorState

    /** PTT pressed, awaiting the server's decision. */
    data object Requesting : FloorState

    /** Transmitting: mic open, burst countdown running. */
    data class Granted(val burstStartedAtMs: Long, val maxBurstMs: Long) : FloorState {
        fun remainingMs(nowMs: Long): Long = (burstStartedAtMs + maxBurstMs - nowMs).coerceAtLeast(0)
    }

    /** Waiting to speak: position in the server's priority queue. */
    data class Queued(val position: Int) : FloorState

    /** Denied with a reason toast; transient until acknowledged or re-requested. */
    data class Denied(val reason: String, val queuePosition: Int) : FloorState

    /** Audio cut mid-burst by a higher priority; transient until released or re-requested. */
    data class Preempted(val by: String, val emergency: Boolean) : FloorState
}

/** One step of work the machine hands back with a transition. */
sealed interface FloorEffect {
    /** Send a FloorRequest frame. */
    data class SendFloorRequest(val callId: String, val priority: Int, val emergency: Boolean) : FloorEffect

    /** Send a FloorReleased frame. */
    data class SendFloorRelease(val callId: String) : FloorEffect

    /** Open the mic / start the burst countdown (granted). */
    data class StartTransmit(val callId: String) : FloorEffect

    /** Cut the mic immediately (pre-empted, revoked, expired, released, call ended). */
    data class StopTransmit(val callId: String) : FloorEffect
}

/**
 * Events driving the machine. Wire events carry the frame contents; local
 * events (press, release, tick) come from the UI/service.
 */
sealed interface FloorEvent {
    data class PttPressed(val callId: String, val atMs: Long, val priority: Int, val emergency: Boolean = false) : FloorEvent
    data class PttReleased(val callId: String, val atMs: Long) : FloorEvent

    /** Drives the burst countdown while Granted; expires the burst at the cap. */
    data class Tick(val callId: String, val atMs: Long) : FloorEvent

    data class GrantReceived(val callId: String, val userId: String, val atMs: Long, val maxBurstMs: Long = DEFAULT_MAX_BURST_MS) : FloorEvent
    data class DeniedReceived(val callId: String, val reason: String, val queuePosition: Int, val atMs: Long) : FloorEvent
    data class PreemptedReceived(val callId: String, val by: String, val emergency: Boolean, val atMs: Long) : FloorEvent
    data class ReleaseEchoReceived(val callId: String, val userId: String, val atMs: Long) : FloorEvent
    data class CallEndedReceived(val callId: String, val atMs: Long) : FloorEvent

    companion object {
        /** Spec default: 60 s bursts, server-configurable; emergency exempt server-side. */
        const val DEFAULT_MAX_BURST_MS: Long = 60_000L
    }
}

/** The machine's full answer to one event: next state + ordered effects. */
data class Transition(val state: FloorState, val effects: List<FloorEffect>)

/**
 * One call's floor state machine.
 */
class FloorStateMachine(private val myUserId: String) {

    var state: FloorState = FloorState.Idle
        private set

    /** Reduce one event to (nextState, effects). Pure and total. */
    fun onEvent(event: FloorEvent): Transition {
        val next = step(event)
        state = next.state
        return next
    }

    private fun step(event: FloorEvent): Transition = when (event) {
        is FloorEvent.PttPressed -> onPress(event)
        is FloorEvent.PttReleased -> onRelease(event)
        is FloorEvent.Tick -> onTick(event)
        is FloorEvent.GrantReceived -> onGrant(event)
        is FloorEvent.DeniedReceived -> onDenied(event)
        is FloorEvent.PreemptedReceived -> onPreempted(event)
        is FloorEvent.ReleaseEchoReceived -> onReleaseEcho(event)
        is FloorEvent.CallEndedReceived -> onCallEnded(event)
    }

    private fun onPress(e: FloorEvent.PttPressed): Transition = when (val s = state) {
        is FloorState.Idle, is FloorState.Denied, is FloorState.Preempted ->
            // Spec: after pre-emption the original speaker re-joins the
            // queue by pressing again — an emergency pre-emption keeps the
            // emergency flag on the re-request.
            Transition(
                FloorState.Requesting,
                listOf(FloorEffect.SendFloorRequest(e.callId, e.priority, e.emergency || (s as? FloorState.Preempted)?.emergency == true)),
            )

        // Presses while a request/grant/queue is already live are no-ops —
        // exactly one request in flight per talker.
        is FloorState.Requesting, is FloorState.Granted, is FloorState.Queued -> Transition(s, emptyList())
    }

    private fun onRelease(e: FloorEvent.PttReleased): Transition = when (val s = state) {
        is FloorState.Granted ->
            Transition(FloorState.Idle, listOf(FloorEffect.SendFloorRelease(e.callId), FloorEffect.StopTransmit(e.callId)))

        // A queued or requesting user releasing PTT cancels their wait.
        is FloorState.Queued, is FloorState.Requesting ->
            Transition(FloorState.Idle, listOf(FloorEffect.SendFloorRelease(e.callId)))

        // Denied / pre-empted acknowledge back to idle; nothing to send.
        is FloorState.Idle, is FloorState.Denied, is FloorState.Preempted -> Transition(s, emptyList())
    }

    private fun onTick(e: FloorEvent.Tick): Transition {
        val s = state
        if (s is FloorState.Granted && e.atMs >= s.burstStartedAtMs + s.maxBurstMs) {
            // Mirror of the server's max-duration auto-release: the client
            // resets its own button even if the echo frame is still in
            // flight — the next authoritative frame reconciles.
            return Transition(FloorState.Idle, listOf(FloorEffect.SendFloorRelease(e.callId), FloorEffect.StopTransmit(e.callId)))
        }
        return Transition(s, emptyList())
    }

    private fun onGrant(e: FloorEvent.GrantReceived): Transition = when (val s = state) {
        is FloorState.Requesting, is FloorState.Queued, is FloorState.Denied ->
            if (e.userId == myUserId) {
                Transition(FloorState.Granted(e.atMs, e.maxBurstMs), listOf(FloorEffect.StartTransmit(e.callId)))
            } else {
                Transition(s, emptyList()) // someone else took the floor while I waited
            }

        is FloorState.Granted ->
            if (e.userId == myUserId) {
                Transition(s, emptyList()) // duplicate grant echo — idempotent
            } else {
                // The server stripped my floor for a peer without a
                // pre-emption notice (dispatcher revoke): the media gate
                // closes regardless — same visible effect as pre-emption.
                Transition(FloorState.Preempted(e.userId, false), listOf(FloorEffect.StopTransmit(e.callId)))
            }

        is FloorState.Idle, is FloorState.Preempted ->
            if (e.userId == myUserId) {
                Transition(FloorState.Granted(e.atMs, e.maxBurstMs), listOf(FloorEffect.StartTransmit(e.callId)))
            } else {
                Transition(FloorState.Idle, emptyList()) // live indicator: someone else speaks
            }
    }

    private fun onDenied(e: FloorEvent.DeniedReceived): Transition = when (val s = state) {
        is FloorState.Requesting, is FloorState.Queued -> Transition(FloorState.Denied(e.reason, e.queuePosition), emptyList())
        is FloorState.Idle, is FloorState.Denied, is FloorState.Preempted, is FloorState.Granted -> Transition(s, emptyList())
    }

    private fun onPreempted(e: FloorEvent.PreemptedReceived): Transition = when (val s = state) {
        is FloorState.Granted ->
            if (e.by == myUserId) Transition(s, emptyList())
            else Transition(FloorState.Preempted(e.by, e.emergency), listOf(FloorEffect.StopTransmit(e.callId)))

        is FloorState.Idle, is FloorState.Requesting, is FloorState.Queued, is FloorState.Denied, is FloorState.Preempted ->
            Transition(FloorState.Preempted(e.by, e.emergency), emptyList())
    }

    private fun onReleaseEcho(e: FloorEvent.ReleaseEchoReceived): Transition = when (val s = state) {
        is FloorState.Granted ->
            if (e.userId == myUserId) {
                Transition(FloorState.Idle, listOf(FloorEffect.StopTransmit(e.callId)))
            } else {
                Transition(s, emptyList())
            }
        is FloorState.Idle, is FloorState.Requesting, is FloorState.Queued, is FloorState.Denied, is FloorState.Preempted ->
            Transition(s, emptyList())
    }

    private fun onCallEnded(e: FloorEvent.CallEndedReceived): Transition = when (val s = state) {
        is FloorState.Granted -> Transition(FloorState.Idle, listOf(FloorEffect.StopTransmit(e.callId)))
        else -> Transition(FloorState.Idle, emptyList())
    }

    /** Reconnects restore a clean slate; the server's live state re-syncs via the next frames. */
    fun reset() {
        state = FloorState.Idle
    }
}
