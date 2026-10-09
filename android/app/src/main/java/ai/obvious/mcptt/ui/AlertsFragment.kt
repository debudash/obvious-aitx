package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import ai.obvious.mcptt.emergency.AlertDelivery
import android.content.Context
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import kotlinx.coroutines.launch

/** Own emergency history: delivery state per alert, ack loop visible. */
class AlertsFragment : Fragment() {
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
            setBackgroundColor(color(R.color.ops_background))
        }
        val list = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        root.addView(list)

        app.appScope.launch {
            app.controller.alertDeliveries.collect { deliveries -> render(list, deliveries) }
        }
        app.appScope.launch {
            app.controller.liveEmergency.collect { emergency ->
                if (emergency != null) {
                    root.addView(
                        TextView(ctx).apply {
                            text = "⚠ ${emergency.alertId}"
                            setTextColor(color(R.color.ops_emergency))
                        },
                    )
                }
            }
        }
        return root
    }

    private fun render(list: LinearLayout, deliveries: List<AlertDelivery>) {
        val ctx = requireContext()
        list.removeAllViews()
        if (deliveries.isEmpty()) return
        deliveries.forEach { delivery ->
            val text = when (delivery) {
                is AlertDelivery.Retrying -> getString(R.string.alert_retrying) + " • " + delivery.alert.alertId
                is AlertDelivery.DeliveredAwaitingAck -> getString(R.string.alert_pending_ack) + " • " + delivery.alert.alertId
                is AlertDelivery.Failed -> getString(R.string.alert_failed) + " • " + delivery.reason
            }
            list.addView(
                TextView(ctx).apply {
                    this.text = text
                    setTextColor(
                        color(
                            when (delivery) {
                                is AlertDelivery.Failed -> R.color.ops_denied
                                is AlertDelivery.DeliveredAwaitingAck -> R.color.ops_queue
                                is AlertDelivery.Retrying -> R.color.ops_queue
                            },
                        ),
                    )
                },
            )
        }
    }

    private fun color(res: Int): Int = ContextCompat.getColor(requireContext(), res)
}
