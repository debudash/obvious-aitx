package ai.obvious.mcptt

import ai.obvious.mcptt.api.McpttApi
import ai.obvious.mcptt.emergency.AlertStore
import ai.obvious.mcptt.store.JsonMissedCallStore
import ai.obvious.mcptt.store.MissedCallStore
import android.app.Application
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit

/** Minimal service locator: one client, one session, one controller. */
class McpttApp : Application() {

    val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    lateinit var session: Session
        private set

    lateinit var api: McpttApi
        private set

    lateinit var controller: McpttController
        private set

    lateinit var missedCallStore: MissedCallStore
        private set

    override fun onCreate() {
        super.onCreate()
        session = Session(this)
        val httpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .pingInterval(20, TimeUnit.SECONDS) // keep half-open NATs honest
            .build()
        api = McpttApi(httpClient, session.serverUrl)
        api.authToken = session.token
        missedCallStore = JsonMissedCallStore(sharedPrefsBacking("mcptt_missed_calls"))
        val alertStore = sharedPrefsAlertStore("mcptt_pending_alerts")
        controller = McpttController(api, httpClient, appScope, alertStore, missedCallStore)
        if (session.isLoggedIn && api.baseUrl.isNotEmpty()) {
            // Resume the previous session; the server validates the token
            // on first request and the login screen re-prompts on 401.
            restoreSession()
        }
    }

    private fun restoreSession() {
        val app = appScope
        app.launch {
            // Token refresh probe happens lazily; reconnect with what we have.
            val token = session.token ?: return@launch
            val user = controller.me ?: runCatching {
                // Fetch my identity from the roster by matching later; the
                // server has no /me-by-token, so reuse the stored username.
                api.listUsers().firstOrNull { it.username == session.username }
            }.getOrNull() ?: return@launch
            controller.startSession(user, token)
        }
    }

    private fun sharedPrefsBacking(name: String) = object : JsonMissedCallStore.Backing {
        private val prefs = getSharedPreferences(name, MODE_PRIVATE)
        override fun read(): String? = prefs.getString("json", null)
        override fun write(json: String?) = prefs.edit().putString("json", json).apply()
    }

    private fun sharedPrefsAlertStore(name: String): AlertStore {
        val prefs = getSharedPreferences(name, MODE_PRIVATE)
        return object : AlertStore {
            override fun load(): List<ai.obvious.mcptt.emergency.PendingAlert> =
                prefs.getString("json", null)?.let { raw ->
                    runCatching {
                        val arr = org.json.JSONArray(raw)
                        (0 until arr.length()).map { i ->
                            val obj = arr.getJSONObject(i)
                            ai.obvious.mcptt.emergency.PendingAlert(
                                alertId = obj.getString("alertId"),
                                imminentPeril = obj.getBoolean("imminentPeril"),
                                lat = if (obj.has("lat")) obj.getDouble("lat") else null,
                                lon = if (obj.has("lon")) obj.getDouble("lon") else null,
                                note = obj.getString("note"),
                                createdAtMs = obj.getLong("createdAtMs"),
                                attemptCount = obj.getInt("attemptCount"),
                            )
                        }
                    }.getOrDefault(emptyList())
                } ?: emptyList()

            override fun save(alerts: List<ai.obvious.mcptt.emergency.PendingAlert>) {
                val arr = org.json.JSONArray()
                alerts.forEach { alert ->
                    val obj = org.json.JSONObject()
                        .put("alertId", alert.alertId)
                        .put("imminentPeril", alert.imminentPeril)
                        .put("note", alert.note)
                        .put("createdAtMs", alert.createdAtMs)
                        .put("attemptCount", alert.attemptCount)
                    alert.lat?.let { obj.put("lat", it) }
                    alert.lon?.let { obj.put("lon", it) }
                    arr.put(obj)
                }
                prefs.edit().putString("json", arr.toString()).apply()
            }
        }
    }
}
