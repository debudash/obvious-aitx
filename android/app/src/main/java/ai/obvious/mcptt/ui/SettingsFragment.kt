package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import ai.obvious.mcptt.signal.SignalState
import android.content.Intent
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.TextView
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import kotlinx.coroutines.launch

/** Affiliations, priority display, alias, connection state, logout. */
class SettingsFragment : Fragment() {
    private val app get() = requireActivity().application as McpttApp

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View {
        val ctx = requireContext()
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(48, 48, 48, 48)
            setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_background))
        }

        val identity = TextView(ctx).apply { setTextColor(ContextCompat.getColor(ctx, R.color.ops_text)) }
        root.addView(identity)
        val connection = TextView(ctx).apply { setTextColor(ContextCompat.getColor(ctx, R.color.ops_text_dim)) }
        root.addView(connection)

        root.addView(sectionLabel(ctx, R.string.affiliations))
        val affList = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        root.addView(affList)

        root.addView(sectionLabel(ctx, R.string.logout))
        root.addView(
            Button(ctx).apply {
                text = getString(R.string.logout)
                setOnClickListener { logout() }
            },
        )

        app.controller.me?.let { user ->
            identity.text = "${user.username} • P${user.priority} • ${user.functionalAlias ?: "—"}"
        }
        app.appScope.launch {
            app.controller.signalState.collect { state ->
                connection.text = when (state) {
                    SignalState.Connected -> getString(R.string.connection_connected)
                    SignalState.Connecting -> getString(R.string.connection_connecting)
                    is SignalState.Retrying -> getString(R.string.connection_retrying, (state.delayMs / 1000) + 1)
                    SignalState.Disconnected -> getString(R.string.connection_offline)
                }
            }
        }
        app.appScope.launch {
            app.controller.groups.collect { groups -> renderAffiliations(affList, groups) }
        }
        return root
    }

    private fun renderAffiliations(list: LinearLayout, groups: List<ai.obvious.mcptt.model.Group>) {
        val ctx = requireContext()
        list.removeAllViews()
        val affiliations = app.controller.affiliations.value
        groups.forEach { group ->
            val row = LinearLayout(ctx).apply { orientation = LinearLayout.HORIZONTAL }
            val label = TextView(ctx).apply {
                text = group.name
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                setTextColor(ContextCompat.getColor(ctx, R.color.ops_text))
            }
            row.addView(label)
            val toggle = Switch(ctx).apply {
                val affiliated = affiliations[group.id] == ai.obvious.mcptt.protocol.AffiliationChanged.STATE_AFFILIATED
                isChecked = affiliated
                setOnCheckedChangeListener { _, checked -> app.controller.setAffiliated(group.id, checked) }
            }
            row.addView(toggle)
            list.addView(row)
        }
    }

    private fun sectionLabel(ctx: android.content.Context, res: Int): TextView =
        TextView(ctx).apply {
            text = getString(res)
            setTextColor(ContextCompat.getColor(ctx, R.color.ops_text_dim))
        }

    private fun logout() {
        app.controller.stopSession()
        app.session.clear()
        startActivity(
            Intent(requireContext(), LoginActivity::class.java)
                .putExtra(LoginActivity.EXTRA_FORCE_LOGIN, true),
        )
        requireActivity().finish()
    }
}
