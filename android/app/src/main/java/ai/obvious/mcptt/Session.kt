package ai.obvious.mcptt

import android.content.Context

/** SharedPreferences-backed session: credentials + server URL. */
class Session(context: Context) {
    private val prefs = context.getSharedPreferences("mcptt_session", Context.MODE_PRIVATE)

    var serverUrl: String
        get() = prefs.getString(KEY_SERVER, "") ?: ""
        set(value) = prefs.edit().putString(KEY_SERVER, value.trimEnd('/')).apply()

    var username: String
        get() = prefs.getString(KEY_USER, "") ?: ""
        set(value) = prefs.edit().putString(KEY_USER, value).apply()

    var token: String?
        get() = prefs.getString(KEY_TOKEN, null)
        set(value) = prefs.edit().putString(KEY_TOKEN, value).apply()

    val isLoggedIn: Boolean get() = token != null && serverUrl.isNotEmpty()

    fun clear() = prefs.edit().clear().apply()

    private companion object {
        const val KEY_SERVER = "server_url"
        const val KEY_USER = "username"
        const val KEY_TOKEN = "token"
    }
}
