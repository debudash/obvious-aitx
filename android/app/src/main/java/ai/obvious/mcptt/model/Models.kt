package ai.obvious.mcptt.model

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** Wire shapes matching mcptt/server/internal/api/dto.go exactly. */

@Serializable
data class User(
    @SerialName("id") val id: String,
    @SerialName("username") val username: String,
    @SerialName("displayName") val displayName: String,
    @SerialName("role") val role: String,
    @SerialName("priority") val priority: Int,
    @SerialName("functionalAlias") val functionalAlias: String? = null,
    @SerialName("createdAt") val createdAt: String? = null,
) {
    /** Named tier per the spec's priority ladder — what the roster renders. */
    val band: String
        get() = when {
            priority >= 10 -> "net control"
            priority >= 9 -> "emergency"
            priority >= 7 -> "supervisor"
            priority >= 4 -> "normal"
            else -> "ambient"
        }
}

@Serializable
data class Group(
    @SerialName("id") val id: String,
    @SerialName("name") val name: String,
    @SerialName("description") val description: String? = null,
    @SerialName("createdBy") val createdBy: String? = null,
    @SerialName("createdAt") val createdAt: String? = null,
)

@Serializable
data class Affiliation(
    @SerialName("userId") val userId: String,
    @SerialName("groupId") val groupId: String,
    @SerialName("state") val state: String,
    @SerialName("changedAt") val changedAt: String? = null,
) {
    val isAffiliated: Boolean get() = state == "affiliated"
}

@Serializable
data class Alert(
    @SerialName("id") val id: String,
    @SerialName("userId") val userId: String,
    @SerialName("callId") val callId: String? = null,
    @SerialName("kind") val kind: String,
    @SerialName("lat") val lat: Double? = null,
    @SerialName("lon") val lon: Double? = null,
    @SerialName("note") val note: String? = null,
    @SerialName("status") val status: String,
    @SerialName("acknowledgedBy") val acknowledgedBy: String? = null,
    @SerialName("createdAt") val createdAt: String? = null,
) {
    val isEmergencyKind: Boolean get() = kind == "emergency" || kind == "imminent-peril"
}

@Serializable
data class LoginResponse(
    @SerialName("user") val user: User,
    @SerialName("token") val token: String,
)

@Serializable
data class EmergencyCallResponse(
    @SerialName("call") val call: CallSession,
)

/** Client-visible call state (callcontrol.SessionInfo rendered by the API). */
@Serializable
data class CallSession(
    @SerialName("callId") val callId: String? = null,
    @SerialName("id") val id: String? = null,
    @SerialName("kind") val kind: String? = null,
    @SerialName("groupId") val groupId: String? = null,
    @SerialName("speaker") val speaker: String? = null,
    @SerialName("queue") val queue: List<String> = emptyList(),
    @SerialName("participants") val participants: List<String> = emptyList(),
    @SerialName("emergency") val emergency: Boolean = false,
) {
    val sessionId: String get() = callId ?: id ?: ""
}
