package ai.obvious.mcptt

import ai.obvious.mcptt.api.McpttApi
import ai.obvious.mcptt.emergency.AlertDelivery
import ai.obvious.mcptt.emergency.AlertRetryQueue
import ai.obvious.mcptt.floor.FloorEffect
import ai.obvious.mcptt.floor.FloorEvent
import ai.obvious.mcptt.floor.FloorState
import ai.obvious.mcptt.floor.FloorStateMachine
import ai.obvious.mcptt.model.Affiliation
import ai.obvious.mcptt.model.Group
import ai.obvious.mcptt.model.User
import ai.obvious.mcptt.protocol.AffiliationChanged
import ai.obvious.mcptt.protocol.CallEnded
import ai.obvious.mcptt.protocol.CallJoined
import ai.obvious.mcptt.protocol.CallKind
import ai.obvious.mcptt.protocol.CallStart
import ai.obvious.mcptt.protocol.EmergencyAlert
import ai.obvious.mcptt.protocol.EmergencyAlertAck
import ai.obvious.mcptt.protocol.FloorDenied
import ai.obvious.mcptt.protocol.FloorGranted
import ai.obvious.mcptt.protocol.FloorPreempted
import ai.obvious.mcptt.protocol.FloorReleased
import ai.obvious.mcptt.protocol.FloorRequest
import ai.obvious.mcptt.protocol.McpttMessage
import ai.obvious.mcptt.protocol.MediaAnswer
import ai.obvious.mcptt.protocol.ParticipantRemoved
import ai.obvious.mcptt.protocol.PresenceUpdate
import ai.obvious.mcptt.signal.SignalListener
import ai.obvious.mcptt.store.MissedCallStore
import ai.obvious.mcptt.signal.SignalState
import ai.obvious.mcptt.signal.SignalingClient
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.util.UUID
import java.util.concurrent.atomic.AtomicReference

/** One live call as the UI renders it. */
data class ActiveCall(
    val callId: String,
    val kind: String,
    val groupId: String?,
    val speaker: String?,
    val participants: List<String>,
    val emergency: Boolean,
)

/** A private call that ended before acceptance — the Contacts list entry. */
data class MissedCall(val callId: String, val fromUserId: String, val atMs: Long)

/**
 * App-level orchestrator: one signaling socket, one floor machine, one
 * alert retry queue, and the REST session. UI screens and the PTT service
 * observe its StateFlows; nothing else touches the socket directly.
 *
 * Wire events arrive on OkHttp's WS thread and are funneled through the
 * main scope, so floor state applies on one thread by construction.
 */
class McpttController(
    val api: McpttApi,
    private val httpClient: okhttp3.OkHttpClient,
    private val scope: CoroutineScope,
    private val alertStore: ai.obvious.mcptt.emergency.AlertStore,
    private val missedCallStore: MissedCallStore,
    private val nowMs: () -> Long = System::currentTimeMillis,
) : SignalListener {

    lateinit var signaling: SignalingClient
        private set

    @Volatile
    var me: User? = null
        private set

    /** Media bridge the PttService installs (mic + audio focus + WebRTC). */
    interface MediaControl {
        fun startTransmit(callId: String)
        fun stopTransmit(callId: String)
        fun onMediaAnswer(callId: String, sdp: String)
        fun leaveCall()
    }

    @Volatile
    var mediaControl: MediaControl? = null

    // ---- observable state ----
    private val _signalState = MutableStateFlow<SignalState>(SignalState.Disconnected)
    val signalState: StateFlow<SignalState> = _signalState

    private val _floorState = MutableStateFlow<FloorState>(FloorState.Idle)
    val floorState: StateFlow<FloorState> = _floorState

    private val _activeCall = MutableStateFlow<ActiveCall?>(null)
    val activeCall: StateFlow<ActiveCall?> = _activeCall

    private val _presence = MutableStateFlow<Map<String, String>>(emptyMap())
    val presence: StateFlow<Map<String, String>> = _presence

    private val _affiliations = MutableStateFlow<Map<String, String>>(emptyMap())
    val affiliations: StateFlow<Map<String, String>> = _affiliations

    private val _roster = MutableStateFlow<List<User>>(emptyList())
    val roster: StateFlow<List<User>> = _roster

    private val _groups = MutableStateFlow<List<Group>>(emptyList())
    val groups: StateFlow<List<Group>> = _groups

    private val _missedCalls = MutableStateFlow<List<MissedCall>>(emptyList())
    val missedCalls: StateFlow<List<MissedCall>> = _missedCalls

    private val _alertDeliveries = MutableStateFlow<List<AlertDelivery>>(emptyList())
    val alertDeliveries: StateFlow<List<AlertDelivery>> = _alertDeliveries

    private val _liveEmergency = MutableStateFlow<EmergencyAlert?>(null)
    val liveEmergency: StateFlow<EmergencyAlert?> = _liveEmergency

    // ---- internal ----
    private val machine = AtomicReference<FloorStateMachine?>(null)
    private val alertQueue: AlertRetryQueue by lazy {
        AlertRetryQueue(
            transport = { alert ->
                api.raiseAlert(alert.imminentPeril, alert.lat, alert.lon, alert.note)
                alert.alertId
            },
            store = alertStore,
            onChanged = { _ ->
                scope.launch { _alertDeliveries.value = alertQueue.snapshot() }
            },
        )
    }

    fun startSession(user: User, token: String) {
        api.authToken = token
        me = user
        machine.set(FloorStateMachine(user.id))
        _missedCalls.value = missedCallStore.load()
        alertQueue.start()
        signaling = SignalingClient(
            baseUrl = api.baseUrl,
            httpClient = httpClient,
            tokenProvider = { api.authToken },
            listener = this,
        )
        signaling.connect()
        refreshRoster()
    }

    fun stopSession() {
        if (::signaling.isInitialized) signaling.close()
        me = null
        machine.set(null)
        _activeCall.value = null
        _floorState.value = FloorState.Idle
    }

    // ---- outbound actions (UI / service) ----

    fun pttPressed(callId: String) {
        val user = me ?: return
        dispatch(FloorEvent.PttPressed(callId, nowMs(), user.priority))
    }

    fun pttReleased(callId: String) {
        dispatch(FloorEvent.PttReleased(callId, nowMs()))
    }

    fun startGroupCall(groupId: String) {
        val user = me ?: return
        send(CallStart(callId = newCallId(), groupId = groupId, kind = CallKind.GROUP.wire, initiatorId = user.id))
    }

    fun startPrivateCall(calleeId: String) {
        val user = me ?: return
        send(CallStart(callId = newCallId(), groupId = calleeId, kind = CallKind.PRIVATE.wire, initiatorId = user.id))
    }

    /** Emergency press-and-hold confirmed: alert always, call optionally. */
    fun triggerEmergency(imminentPeril: Boolean, withCall: Boolean, groupId: String?, lat: Double?, lon: Double?, note: String) {
        val alertId = "alert-${UUID.randomUUID()}"
        alertQueue.submit(
            ai.obvious.mcptt.emergency.PendingAlert(
                alertId = alertId,
                imminentPeril = imminentPeril,
                lat = lat,
                lon = lon,
                note = note,
                createdAtMs = nowMs(),
            ),
        )
        if (withCall && groupId != null) {
            scope.launch(Dispatchers.IO) {
                try {
                    api.startEmergencyGroupCall(groupId, imminentPeril, lat, lon, note)
                } catch (e: Exception) {
                    // The voiceless alert is already queued for retry, so a
                    // failed emergency call still reaches dispatch.
                }
            }
        }
    }

    fun setAffiliated(groupId: String, affiliated: Boolean) {
        scope.launch(Dispatchers.IO) {
            try {
                if (affiliated) api.affiliate(groupId) else api.deaffiliate(groupId)
                // AffiliationChanged frame confirms within one round trip;
                // the optimistic echo keeps the toggle responsive offline.
                _affiliations.value = _affiliations.value +
                    (groupId to if (affiliated) AffiliationChanged.STATE_AFFILIATED else AffiliationChanged.STATE_DEAFFILIATED)
            } catch (e: Exception) {
                refreshRoster() // resync on failure
            }
        }
    }

    fun refreshRoster() {
        scope.launch(Dispatchers.IO) {
            try {
                _roster.value = api.listUsers()
                _groups.value = api.listGroups()
                _affiliations.value = api.listAffiliations().associate { it.groupId to it.state }
            } catch (e: Exception) {
                // Stale data stays visible; the connection banner shows state.
            }
        }
    }

    // ---- floor machine plumbing ----

    private fun dispatch(event: FloorEvent) {
        val m = machine.get() ?: return
        val transition = m.onEvent(event)
        applyEffects(transition.effects, event)
        _floorState.value = m.state
    }

    private fun applyEffects(effects: List<FloorEffect>, event: FloorEvent) {
        val user = me
        for (effect in effects) {
            when (effect) {
                is FloorEffect.SendFloorRequest -> {
                    if (effect.callId.isNotEmpty()) {
                        send(
                            FloorRequest(
                                callId = effect.callId,
                                userId = user?.id ?: "",
                                priority = effect.priority,
                                emergency = effect.emergency,
                            ),
                        )
                    }
                }
                is FloorEffect.SendFloorRelease -> {
                    if (effect.callId.isNotEmpty()) {
                        send(FloorReleased(callId = effect.callId, userId = user?.id ?: ""))
                    }
                }
                is FloorEffect.StartTransmit -> mediaControl?.startTransmit(effect.callId)
                is FloorEffect.StopTransmit -> mediaControl?.stopTransmit(effect.callId)
            }
        }
    }

    private fun send(message: McpttMessage): Boolean = ::signaling.isInitialized && signaling.send(message)

    /** The WebRTC engine's SDP offer, sent over the signaling socket. */
    fun sendMediaOffer(message: MediaAnswer): Boolean = send(message)

    // ---- SignalListener ----

    override fun onStateChange(state: SignalState) {
        scope.launch { _signalState.value = state }
    }

    override fun onReconnected() {
        machine.get()?.reset()
        refreshRoster()
    }

    override fun onMessage(message: McpttMessage) {
        scope.launch(Dispatchers.Main) { handleWire(message) }
    }

    private fun handleWire(message: McpttMessage) {
        val myId = me?.id ?: return
        when (message) {
            is FloorGranted -> {
                updateSpeaker(message.callId, message.userId)
                dispatch(FloorEvent.GrantReceived(message.callId, message.userId, nowMs(), FloorEvent.DEFAULT_MAX_BURST_MS))
            }
            is FloorDenied -> dispatch(FloorEvent.DeniedReceived(message.callId, message.reason, message.queuePosition, nowMs()))
            is FloorPreempted -> dispatch(FloorEvent.PreemptedReceived(message.callId, message.by, message.emergency, nowMs()))
            is FloorReleased -> dispatch(FloorEvent.ReleaseEchoReceived(message.callId, message.userId, nowMs()))
            is CallStart -> {
                // Server fan-out announces every call, including my own.
                _activeCall.value = ActiveCall(
                    callId = message.callId,
                    kind = message.kind,
                    groupId = message.groupId,
                    speaker = null,
                    participants = listOfNotNull(message.initiatorId),
                    emergency = false,
                )
                machine.get()?.reset()
            }
            is CallJoined -> _activeCall.value?.let { call ->
                if (call.callId == message.callId) {
                    _activeCall.value = call.copy(participants = (call.participants + message.userId).distinct())
                }
            }
            is ParticipantRemoved -> {
                val call = _activeCall.value ?: return
                if (message.callId != call.callId) return
                if (message.userId == myId) {
                    leaveCall()
                } else {
                    _activeCall.value = call.copy(participants = call.participants - message.userId)
                }
            }
            is CallEnded -> {
                val call = _activeCall.value
                if (call?.callId == message.callId) {
                    // Private call that ended without my acceptance → missed.
                    if (call.kind == CallKind.PRIVATE.wire && message.by != myId) {
                        recordMissedCall(call.callId, call.participants.firstOrNull { it != myId } ?: message.by)
                    }
                }
                dispatch(FloorEvent.CallEndedReceived(message.callId, nowMs()))
                leaveCall()
            }
            is EmergencyAlert -> _liveEmergency.value = message
            is EmergencyAlertAck -> {
                alertQueue.onAlertAck(message.alertId)
                _alertDeliveries.value = alertQueue.snapshot()
                if (_liveEmergency.value?.alertId == message.alertId) {
                    _liveEmergency.value = null
                }
            }
            is PresenceUpdate -> _presence.value = _presence.value + (message.userId to message.state)
            is AffiliationChanged -> _affiliations.value = _affiliations.value + (message.groupId to message.state)
            is MediaAnswer -> message.sdp?.let { sdp -> mediaControl?.onMediaAnswer(message.callId, sdp) }
            else -> {} // FloorRequest/FloorRevoke/etc. are client→server frames
        }
    }

    private fun updateSpeaker(callId: String, userId: String) {
        val call = _activeCall.value ?: return
        if (call.callId == callId) {
            _activeCall.value = call.copy(speaker = userId)
        }
    }

    private fun leaveCall() {
        _activeCall.value = null
        mediaControl?.leaveCall()
        machine.get()?.reset()
        _floorState.value = FloorState.Idle
    }

    private fun recordMissedCall(callId: String, fromUserId: String) {
        missedCallStore.add(MissedCall(callId, fromUserId, nowMs()))
        _missedCalls.value = missedCallStore.load()
    }

    private fun newCallId(): String = "call-${UUID.randomUUID()}"

    companion object {
        /** Server default (spec): 60 s bursts, emergency exempt server-side. */
        const val DEFAULT_MAX_BURST_MS: Long = 60_000L
    }
}
