package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import ai.obvious.mcptt.signal.SignalState
import android.content.Intent
import android.os.Bundle
import android.view.View
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.fragment.app.Fragment
import com.google.android.material.bottomnavigation.BottomNavigationView
import kotlinx.coroutines.launch

/**
 * Four-tab shell: Talk, Contacts, Alerts, Settings. One connection banner
 * across the top reflects the signaling client's lifecycle.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var banner: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val app = application as McpttApp
        if (app.controller.me == null) {
            // Deep-open without a session: back to login.
            startActivity(
                Intent(this, LoginActivity::class.java)
                    .putExtra(LoginActivity.EXTRA_FORCE_LOGIN, true),
            )
            finish()
            return
        }
        setContentView(R.layout.activity_main)
        banner = findViewById(R.id.connection_banner)

        val nav = findViewById<BottomNavigationView>(R.id.bottom_nav)
        nav.setOnItemSelectedListener { item ->
            val fragment: Fragment = when (item.itemId) {
                R.id.nav_contacts -> ContactsFragment()
                R.id.nav_alerts -> AlertsFragment()
                R.id.nav_settings -> SettingsFragment()
                else -> TalkFragment()
            }
            supportFragmentManager.beginTransaction()
                .replace(R.id.fragment_container, fragment)
                .commit()
            true
        }
        if (savedInstanceState == null) {
            nav.selectedItemId = R.id.nav_talk
        }

        app.appScope.launch {
            app.controller.signalState.collect { state -> renderConnection(state) }
        }
    }

    private fun renderConnection(state: SignalState) {
        runOnUiThread {
            when (state) {
                SignalState.Connected -> banner.visibility = View.GONE
                SignalState.Connecting -> showBanner(R.string.connection_connecting)
                is SignalState.Retrying -> showBanner(R.string.connection_retrying, ((state.delayMs / 1000) + 1).toInt())
                SignalState.Disconnected -> showBanner(R.string.connection_offline)
            }
        }
    }

    private fun showBanner(textRes: Int, seconds: Int = 0) {
        banner.text = if (seconds > 0) getString(textRes, seconds) else getString(textRes)
        banner.visibility = View.VISIBLE
    }
}
