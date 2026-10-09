package ai.obvious.mcptt.protocol

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive

/**
 * The WSS JSON contract shared with the Go server
 * (mcptt/server/internal/protocol/protocol.go). Field names and type
 * literals must match that file exactly — both ends decode the same
 * frames. Message set: floor control (TS 24.379 semantics over the
 * spec's documented WebSocket-JSON deviation), call lifecycle,
 * emergency alerts, presence/affiliation fan-out, and media signaling.
 */

const val TYPE_FLOOR_REQUEST = "FloorRequest"
const val TYPE_FLOOR_GRANTED = "FloorGranted"
const val TYPE_FLOOR_DENIED = "FloorDenied"
const val TYPE_FLOOR_RELEASED = "FloorReleased"
const val TYPE_FLOOR_PREEMPTED = "FloorPreempted"
const val TYPE_FLOOR_REVOKE = "FloorRevoke"
const val TYPE_CALL_START = "CallStart"
const val TYPE_CALL_JOINED = "CallJoined"
const val TYPE_CALL_ENDED = "CallEnded"
const val TYPE_EMERGENCY_ALERT = "EmergencyAlert"
const val TYPE_EMERGENCY_ALERT_ACK = "EmergencyAlertAck"
const val TYPE_PARTICIPANT_REMOVED = "ParticipantRemoved"
const val TYPE_PRESENCE_UPDATE = "PresenceUpdate"
const val TYPE_AFFILIATION_CHANGED = "AffiliationChanged"
const val TYPE_MEDIA_OFFER = "MediaOffer"
const val TYPE_MEDIA_ANSWER = "MediaAnswer"

/** Every wire message embeds the envelope: a single "type" literal. */
sealed interface McpttMessage {
    val type: String
}

/** Client → server: ask for the talk floor. */
@Serializable
data class FloorRequest(
    override val type: String = TYPE_FLOOR_REQUEST,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
    @SerialName("priority") val priority: Int,
    @SerialName("emergency") val emergency: Boolean = false,
) : McpttMessage

/** Server → all participants: arbitration result; queue in priority order. */
@Serializable
data class FloorGranted(
    override val type: String = TYPE_FLOOR_GRANTED,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
    @SerialName("queue") val queue: List<String> = emptyList(),
) : McpttMessage

/** Server → requester: rejected, with queue position on spillover. */
@Serializable
data class FloorDenied(
    override val type: String = TYPE_FLOOR_DENIED,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
    @SerialName("reason") val reason: String,
    @SerialName("queuePosition") val queuePosition: Int = 0,
) : McpttMessage

/** Client → server after releasing PTT (echoed to all). */
@Serializable
data class FloorReleased(
    override val type: String = TYPE_FLOOR_RELEASED,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
) : McpttMessage

/** Server → all: a higher-priority floor took the call mid-burst. */
@Serializable
data class FloorPreempted(
    override val type: String = TYPE_FLOOR_PREEMPTED,
    @SerialName("callId") val callId: String,
    @SerialName("by") val by: String,
    @SerialName("emergency") val emergency: Boolean = false,
) : McpttMessage

/** Dispatcher → server: strip the current talker. */
@Serializable
data class FloorRevoke(
    override val type: String = TYPE_FLOOR_REVOKE,
    @SerialName("callId") val callId: String,
    @SerialName("by") val by: String,
    @SerialName("reason") val reason: String = "",
) : McpttMessage

enum class CallKind(val wire: String) {
    GROUP("group"),
    PRIVATE("private"),
    BROADCAST("broadcast"),
}

/** Client → server: open a call (pre-arranged group, private, or broadcast). */
@Serializable
data class CallStart(
    override val type: String = TYPE_CALL_START,
    @SerialName("callId") val callId: String,
    @SerialName("groupId") val groupId: String? = null,
    @SerialName("kind") val kind: String,
    @SerialName("initiatorId") val initiatorId: String,
) : McpttMessage

/** Server → participants: a party joined (late entry). */
@Serializable
data class CallJoined(
    override val type: String = TYPE_CALL_JOINED,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
) : McpttMessage

/** Server → participants: call torn down. */
@Serializable
data class CallEnded(
    override val type: String = TYPE_CALL_ENDED,
    @SerialName("callId") val callId: String,
    @SerialName("by") val by: String,
) : McpttMessage

/** Server → all: a dispatcher removed a participant from a live call. */
@Serializable
data class ParticipantRemoved(
    override val type: String = TYPE_PARTICIPANT_REMOVED,
    @SerialName("callId") val callId: String,
    @SerialName("userId") val userId: String,
    @SerialName("by") val by: String,
) : McpttMessage

/**
 * Client → server: one-tap (voiceless) alert or the alert accompanying an
 * emergency call. Location is client-reported WGS-84, omitted without a fix.
 */
@Serializable
data class EmergencyAlert(
    override val type: String = TYPE_EMERGENCY_ALERT,
    @SerialName("alertId") val alertId: String,
    @SerialName("userId") val userId: String,
    @SerialName("callId") val callId: String? = null,
    @SerialName("lat") val lat: Double? = null,
    @SerialName("lon") val lon: Double? = null,
    @SerialName("emergency") val emergency: Boolean,
    @SerialName("imminentPeril") val imminentPeril: Boolean = false,
    @SerialName("note") val note: String = "",
) : McpttMessage

/** Server → all: a dispatcher acknowledged an alert. */
@Serializable
data class EmergencyAlertAck(
    override val type: String = TYPE_EMERGENCY_ALERT_ACK,
    @SerialName("alertId") val alertId: String,
    @SerialName("acknowledgedBy") val acknowledgedBy: String,
) : McpttMessage

/** Server → all: a user connected to or dropped from /ws. */
@Serializable
data class PresenceUpdate(
    override val type: String = TYPE_PRESENCE_UPDATE,
    @SerialName("userId") val userId: String,
    @SerialName("state") val state: String,
    @SerialName("at") val at: Long,
) : McpttMessage {
    companion object {
        const val STATE_ONLINE = "online"
        const val STATE_OFFLINE = "offline"
    }
}

/** Server → all: an affiliation transitioned (TS 23.280). */
@Serializable
data class AffiliationChanged(
    override val type: String = TYPE_AFFILIATION_CHANGED,
    @SerialName("userId") val userId: String,
    @SerialName("groupId") val groupId: String,
    @SerialName("state") val state: String,
    @SerialName("at") val at: Long,
) : McpttMessage {
    companion object {
        const val STATE_AFFILIATED = "affiliated"
        const val STATE_DEAFFILIATED = "deaffiliated"
    }
}

/** Client → server: WebRTC offer carrying the call's room-scoped token. */
@Serializable
data class MediaOffer(
    override val type: String = TYPE_MEDIA_OFFER,
    @SerialName("callId") val callId: String,
    @SerialName("token") val token: String,
    @SerialName("sdp") val sdp: String,
) : McpttMessage

/** Server → client: the SFU's answer, or empty SDP + err on rejection. */
@Serializable
data class MediaAnswer(
    override val type: String = TYPE_MEDIA_ANSWER,
    @SerialName("callId") val callId: String,
    @SerialName("sdp") val sdp: String? = null,
    @SerialName("err") val err: String? = null,
) : McpttMessage

/**
 * Decodes one wire frame into its typed message. Unknown types return null
 * (forward-compatible: the server may add messages before this client
 * learns them — same behavior as the console and iOS clients).
 */
object McpttProtocol {

    val json: Json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
        explicitNulls = false
    }

    private val envelope = Json { ignoreUnknownKeys = true }

    /**
     * Decodes one wire frame; malformed or unknown frames return null.
     * The signaling loop must never die on garbage — a frame that cannot
     * be understood is dropped exactly like an unknown type.
     */
    fun decode(raw: String): McpttMessage? = runCatching {
        val type = envelope.decodeFromString(JsonObject.serializer(), raw)["type"]
            ?.jsonPrimitive?.content
            ?: return null
        return when (type) {
            TYPE_FLOOR_REQUEST -> json.decodeFromString(FloorRequest.serializer(), raw)
            TYPE_FLOOR_GRANTED -> json.decodeFromString(FloorGranted.serializer(), raw)
            TYPE_FLOOR_DENIED -> json.decodeFromString(FloorDenied.serializer(), raw)
            TYPE_FLOOR_RELEASED -> json.decodeFromString(FloorReleased.serializer(), raw)
            TYPE_FLOOR_PREEMPTED -> json.decodeFromString(FloorPreempted.serializer(), raw)
            TYPE_FLOOR_REVOKE -> json.decodeFromString(FloorRevoke.serializer(), raw)
            TYPE_CALL_START -> json.decodeFromString(CallStart.serializer(), raw)
            TYPE_CALL_JOINED -> json.decodeFromString(CallJoined.serializer(), raw)
            TYPE_CALL_ENDED -> json.decodeFromString(CallEnded.serializer(), raw)
            TYPE_EMERGENCY_ALERT -> json.decodeFromString(EmergencyAlert.serializer(), raw)
            TYPE_EMERGENCY_ALERT_ACK -> json.decodeFromString(EmergencyAlertAck.serializer(), raw)
            TYPE_PARTICIPANT_REMOVED -> json.decodeFromString(ParticipantRemoved.serializer(), raw)
            TYPE_PRESENCE_UPDATE -> json.decodeFromString(PresenceUpdate.serializer(), raw)
            TYPE_AFFILIATION_CHANGED -> json.decodeFromString(AffiliationChanged.serializer(), raw)
            TYPE_MEDIA_OFFER -> json.decodeFromString(MediaOffer.serializer(), raw)
            TYPE_MEDIA_ANSWER -> json.decodeFromString(MediaAnswer.serializer(), raw)
            else -> null
        }
    }.getOrNull()

    fun encode(message: McpttMessage): String = when (message) {
        is FloorRequest -> json.encodeToString(FloorRequest.serializer(), message)
        is FloorGranted -> json.encodeToString(FloorGranted.serializer(), message)
        is FloorDenied -> json.encodeToString(FloorDenied.serializer(), message)
        is FloorReleased -> json.encodeToString(FloorReleased.serializer(), message)
        is FloorPreempted -> json.encodeToString(FloorPreempted.serializer(), message)
        is FloorRevoke -> json.encodeToString(FloorRevoke.serializer(), message)
        is CallStart -> json.encodeToString(CallStart.serializer(), message)
        is CallJoined -> json.encodeToString(CallJoined.serializer(), message)
        is CallEnded -> json.encodeToString(CallEnded.serializer(), message)
        is ParticipantRemoved -> json.encodeToString(ParticipantRemoved.serializer(), message)
        is EmergencyAlert -> json.encodeToString(EmergencyAlert.serializer(), message)
        is EmergencyAlertAck -> json.encodeToString(EmergencyAlertAck.serializer(), message)
        is PresenceUpdate -> json.encodeToString(PresenceUpdate.serializer(), message)
        is AffiliationChanged -> json.encodeToString(AffiliationChanged.serializer(), message)
        is MediaOffer -> json.encodeToString(MediaOffer.serializer(), message)
        is MediaAnswer -> json.encodeToString(MediaAnswer.serializer(), message)
    }
}
