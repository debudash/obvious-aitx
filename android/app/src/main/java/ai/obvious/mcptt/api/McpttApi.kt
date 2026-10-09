package ai.obvious.mcptt.api

import ai.obvious.mcptt.model.Affiliation
import ai.obvious.mcptt.model.Alert
import ai.obvious.mcptt.model.EmergencyCallResponse
import ai.obvious.mcptt.model.Group
import ai.obvious.mcptt.model.LoginResponse
import ai.obvious.mcptt.model.User
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Non-2xx REST answer; message carries the server's error string. */
class ApiException(val code: Int, message: String) : Exception("HTTP $code: $message") {
    /** Auth failures route the user back to login rather than a retry loop. */
    val isAuthError: Boolean get() = code == 401 || code == 403
}

@Serializable
private data class LoginBody(val username: String, val password: String)

@Serializable
private data class EmergencyCallBody(
    val callId: String? = null,
    val groupId: String? = null,
    val imminentPeril: Boolean = false,
    val lat: Double? = null,
    val lon: Double? = null,
    val note: String = "",
)

/**
 * Synchronous REST client over OkHttp for the MCPTT control plane
 * (mcptt/server/internal/api/router.go). Callers wrap in coroutines.
 * Every request carries the bearer token when one is set.
 */
class McpttApi(
    private val httpClient: OkHttpClient,
    baseUrl: String,
) {
    @Volatile
    var authToken: String? = null

    @Volatile
    var baseUrl: String = baseUrl.trimEnd('/')
        private set

    private val json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
        explicitNulls = false
    }

    fun updateBaseUrl(url: String) {
        baseUrl = url.trimEnd('/')
    }

    // ---- auth ----

    fun login(username: String, password: String): LoginResponse {
        val body = json.encodeToString(LoginBody.serializer(), LoginBody(username, password))
        return decode(sendJson("POST", "$baseUrl/api/auth/login", body), LoginResponse.serializer())
    }

    // ---- roster ----

    fun listUsers(): List<User> = decode(sendGet("$baseUrl/api/users"), ListSerializer(User.serializer()))

    fun me(): User = decode(sendGet("$baseUrl/api/users/me"), User.serializer())

    fun listGroups(): List<Group> = decode(sendGet("$baseUrl/api/groups"), ListSerializer(Group.serializer()))

    fun listAffiliations(): List<Affiliation> = decode(sendGet("$baseUrl/api/affiliations"), ListSerializer(Affiliation.serializer()))

    fun affiliate(groupId: String): Affiliation = decode(
        sendJson("POST", "$baseUrl/api/groups/$groupId/affiliations", "{}"),
        Affiliation.serializer(),
    )

    fun deaffiliate(groupId: String): Affiliation = decode(
        sendJson("DELETE", "$baseUrl/api/groups/$groupId/affiliations", "{}"),
        Affiliation.serializer(),
    )

    // ---- emergency ----

    /**
     * Voiceless alert. AlertRetryQueue retries until a 2xx, then the
     * dispatcher's EmergencyAlertAck frame completes the loop.
     */
    fun raiseAlert(imminentPeril: Boolean, lat: Double?, lon: Double?, note: String): Alert {
        val body = json.encodeToString(
            EmergencyCallBody.serializer(),
            EmergencyCallBody(imminentPeril = imminentPeril, lat = lat, lon = lon, note = note),
        )
        return decode(sendJson("POST", "$baseUrl/api/emergency/alerts", body), Alert.serializer())
    }

    fun listMyAlerts(): List<Alert> = decode(sendGet("$baseUrl/api/emergency/alerts"), ListSerializer(Alert.serializer()))

    /** Emergency group call (priority 9, pre-empts active floors server-side). */
    fun startEmergencyGroupCall(groupId: String, imminentPeril: Boolean, lat: Double?, lon: Double?, note: String): EmergencyCallResponse {
        val body = json.encodeToString(
            EmergencyCallBody.serializer(),
            EmergencyCallBody(groupId = groupId, imminentPeril = imminentPeril, lat = lat, lon = lon, note = note),
        )
        return decode(sendJson("POST", "$baseUrl/api/calls/emergency", body), EmergencyCallResponse.serializer())
    }

    // ---- plumbing ----

    private fun authorized(request: Request): Request {
        val token = authToken ?: return request
        return request.newBuilder().header("Authorization", "Bearer $token").build()
    }

    private fun sendGet(url: String): String {
        val request = authorized(Request.Builder().url(url).get().build())
        return execute(request)
    }

    private fun sendJson(method: String, url: String, body: String): String {
        val request = authorized(Request.Builder().url(url).method(method, body.toRequestBody(JSON)).build())
        return execute(request)
    }

    private fun execute(request: Request): String {
        httpClient.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (!response.isSuccessful) throw ApiException(response.code, errorOf(text, response.code))
            return text
        }
    }

    private fun <T> decode(text: String, serializer: KSerializer<T>): T = json.decodeFromString(serializer, text)

    private fun errorOf(text: String, code: Int): String = try {
        json.decodeFromString(JsonObject.serializer(), text)["error"]
            ?.toString()?.trim('"')
            ?: text.take(200)
    } catch (_: Exception) {
        "HTTP $code"
    }

    private companion object {
        private val JSON = "application/json; charset=utf-8".toMediaType()
    }
}
