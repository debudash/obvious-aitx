package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import ai.obvious.mcptt.api.ApiException
import android.content.Intent
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class LoginActivity : AppCompatActivity() {

    private lateinit var app: McpttApp

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        app = application as McpttApp
        if (app.session.isLoggedIn && intent.getBooleanExtra(EXTRA_FORCE_LOGIN, false).not()) {
            // The app already restored a session; skip the form.
            startActivity(Intent(this, MainActivity::class.java))
            finish()
            return
        }
        setContentView(R.layout.activity_login)

        val server = findViewById<EditText>(R.id.server_url)
        val username = findViewById<EditText>(R.id.username)
        val password = findViewById<EditText>(R.id.password)
        val signIn = findViewById<Button>(R.id.sign_in)
        val error = findViewById<TextView>(R.id.login_error)

        if (app.session.serverUrl.isNotEmpty()) {
            server.setText(app.session.serverUrl)
            username.setText(app.session.username)
        } else {
            // Sandbox default for first run — edit per deployment.
            server.setText("http://10.0.2.2:8080")
        }

        signIn.setOnClickListener {
            val serverUrl = server.text.toString().trim()
            val user = username.text.toString().trim()
            val pass = password.text.toString()
            if (serverUrl.isEmpty() || user.isEmpty()) return@setOnClickListener
            signIn.isEnabled = false
            error.visibility = View.GONE
            app.appScope.launch {
                try {
                    app.api.updateBaseUrl(serverUrl)
                    val response = withContext(Dispatchers.IO) { app.api.login(user, pass) }
                    app.session.serverUrl = serverUrl
                    app.session.username = user
                    app.session.token = response.token
                    app.controller.startSession(response.user, response.token)
                    withContext(Dispatchers.Main) {
                        startActivity(Intent(this@LoginActivity, MainActivity::class.java))
                        finish()
                    }
                } catch (e: Exception) {
                    withContext(Dispatchers.Main) {
                        signIn.isEnabled = true
                        error.visibility = View.VISIBLE
                        error.text = when (e) {
                            is ApiException -> getString(R.string.login_failed) + " — " + e.message
                            else -> getString(R.string.login_failed)
                        }
                    }
                }
            }
        }
    }

    companion object {
        const val EXTRA_FORCE_LOGIN = "force_login"
    }
}
