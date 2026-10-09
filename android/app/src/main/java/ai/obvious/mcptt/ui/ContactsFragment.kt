package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import kotlinx.coroutines.launch

/** Roster with private-call dialing and the missed-call log. */
class ContactsFragment : Fragment() {

    private val app get() = requireActivity().application as McpttApp

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View {
        val ctx = requireContext()
        fun color(res: Int): Int = ContextCompat.getColor(ctx, res)
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(48, 48, 48, 48)
            setBackgroundColor(color(R.color.ops_background))
        }

        val missedTitle = TextView(ctx).apply {
            text = getString(R.string.missed_calls)
            setTextColor(color(R.color.ops_text_dim))
        }
        root.addView(missedTitle)
        val missedList = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        root.addView(missedList)

        val rosterTitle = TextView(ctx).apply {
            text = getString(R.string.tab_contacts)
            setTextColor(color(R.color.ops_text_dim))
        }
        root.addView(rosterTitle)
        val rosterList = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        root.addView(rosterList)

        app.appScope.launch {
            app.controller.missedCalls.collect { missed ->
                missedList.removeAllViews()
                if (missed.isEmpty()) {
                    missedList.addView(
                        TextView(ctx).apply {
                            text = getString(R.string.no_missed_calls)
                            setTextColor(color(R.color.ops_text_dim))
                        },
                    )
                }
                missed.forEach { entry ->
                    missedList.addView(
                        TextView(ctx).apply {
                            text = "• ${entry.fromUserId}"
                            setTextColor(color(R.color.ops_denied))
                        },
                    )
                }
            }
        }
        app.appScope.launch {
            app.controller.roster.collect { users ->
                rosterList.removeAllViews()
                val myId = app.controller.me?.id
                users.filter { it.id != myId }.forEach { user ->
                    val row = LinearLayout(ctx).apply { orientation = LinearLayout.HORIZONTAL }
                    val name = TextView(ctx).apply {
                        text = user.functionalAlias ?: user.username
                        layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                        setTextColor(color(R.color.ops_text))
                    }
                    row.addView(name)
                    val call = Button(ctx).apply {
                        text = getString(R.string.btn_call)
                        setOnClickListener { app.controller.startPrivateCall(user.id) }
                    }
                    row.addView(call)
                    rosterList.addView(row)
                }
            }
        }
        return root
    }
}
