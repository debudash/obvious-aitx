package ai.obvious.mcptt.ui

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.R
import ai.obvious.mcptt.floor.FloorState
import ai.obvious.mcptt.model.Group
import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.LocationManager
import android.os.Bundle
import android.view.LayoutInflater
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * The Talk screen: affiliated groups, the live call card, and the PTT
 * button. The button IS the floor indicator — its label, color, and
 * countdown mirror the server's floor machine state exactly (spec).
 */
class TalkFragment : Fragment() {

    private val app get() = requireActivity().application as McpttApp

    private var groupsList: LinearLayout? = null
    private var callCard: View? = null
    private var callTitle: TextView? = null
    private var callSpeaker: TextView? = null
    private var pttButton: Button? = null
    private var pttLabel: TextView? = null
    private var queueLabel: TextView? = null

    private var emergencyPressStart = 0L
    private var countdownJob: Job? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.fragment_talk, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        groupsList = view.findViewById(R.id.groups_list)
        callCard = view.findViewById(R.id.call_card)
        callTitle = view.findViewById(R.id.call_title)
        callSpeaker = view.findViewById(R.id.call_speaker)
        pttButton = view.findViewById(R.id.ptt_button)
        pttLabel = view.findViewById(R.id.ptt_label)
        queueLabel = view.findViewById(R.id.queue_label)

        wirePtt(view.findViewById(R.id.ptt_button))
        wireEmergency(view.findViewById(R.id.emergency_button))
        view.findViewById<Button>(R.id.btn_end_call).setOnClickListener { endCall() }

        app.appScope.launch {
            app.controller.groups.collect { groups -> renderGroups(groups) }
        }
        app.appScope.launch {
            app.controller.activeCall.collect { call -> renderCall(call) }
        }
        app.appScope.launch {
            app.controller.floorState.collect { floor -> renderFloor(floor) }
        }
    }

    private fun wirePtt(button: Button) {
        button.setOnTouchListener { v, event ->
            val call = app.controller.activeCall.value
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    call?.let { app.controller.pttPressed(it.callId) }
                    true
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    call?.let { app.controller.pttReleased(it.callId) }
                    v.performClick()
                    true
                }
                else -> false
            }
        }
    }

    private fun wireEmergency(button: Button) {
        button.setOnTouchListener { v, event ->
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    emergencyPressStart = System.currentTimeMillis()
                    true
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    if (System.currentTimeMillis() - emergencyPressStart >= EMERGENCY_HOLD_MS) {
                        confirmEmergency()
                    }
                    v.performClick()
                    true
                }
                else -> false
            }
        }
    }

    /** Guarded trigger: hold 1.5 s, then explicit confirmation dialog. */
    private fun confirmEmergency() {
        val withCall = intArrayOf(1) // checkbox state holder for the dialog
        AlertDialog.Builder(requireContext())
            .setTitle(R.string.emergency_confirm_title)
            .setMessage(getString(R.string.emergency_confirm_message, ""))
            .setMultiChoiceItems(
                arrayOf(getString(R.string.emergency_confirm_call)),
                booleanArrayOf(true),
            ) { _, _, isChecked -> withCall[0] = if (isChecked) 1 else 0 }
            .setPositiveButton(R.string.emergency_send) { _, _ ->
                val location = lastLocation()
                app.controller.triggerEmergency(
                    imminentPeril = false,
                    withCall = withCall[0] == 1,
                    groupId = app.controller.affiliations.value.keys.firstOrNull(),
                    lat = location?.first,
                    lon = location?.second,
                    note = "",
                )
            }
            .setNegativeButton(R.string.emergency_cancel, null)
            .show()
    }

    /** Last-known fix if the device granted location permission; null otherwise. */
    private fun lastLocation(): Pair<Double, Double>? {
        val ctx = requireContext()
        val fine = ContextCompat.checkSelfPermission(ctx, Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED
        if (!fine) return null
        val lm = ctx.getSystemService(Context.LOCATION_SERVICE) as? LocationManager ?: return null
        val best = lm.allProviders
            .mapNotNull { lm.getLastKnownLocation(it) }
            .maxByOrNull { it.time } ?: return null
        return best.latitude to best.longitude
    }

    private fun renderGroups(groups: List<Group>) {
        val list = groupsList ?: return
        list.removeAllViews()
        val affiliations = app.controller.affiliations.value
        for (group in groups) {
            val row = LinearLayout(requireContext()).apply {
                orientation = LinearLayout.HORIZONTAL
                setPadding(0, 24, 0, 24)
            }
            val name = TextView(requireContext()).apply {
                text = group.name
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                setTextColor(ContextCompat.getColor(requireContext(), R.color.ops_text))
                textSize = 16f
            }
            row.addView(name)
            val affiliated = affiliations[group.id] == ai.obvious.mcptt.protocol.AffiliationChanged.STATE_AFFILIATED
            val status = TextView(requireContext()).apply {
                text = if (affiliated) getString(R.string.affiliated_label) else getString(R.string.not_affiliated_label)
                setTextColor(
                    ContextCompat.getColor(
                        requireContext(),
                        if (affiliated) R.color.ops_talk else R.color.ops_text_dim,
                    ),
                )
                textSize = 12f
            }
            row.addView(status)
            val action = Button(requireContext()).apply {
                text = if (affiliated) getString(R.string.btn_start) else getString(R.string.btn_join)
                setOnClickListener { app.controller.startGroupCall(group.id) }
            }
            row.addView(action)
            list.addView(row)
        }
        if (groups.isEmpty()) {
            val empty = TextView(requireContext()).apply {
                text = getString(R.string.ptt_no_call)
                setTextColor(ContextCompat.getColor(requireContext(), R.color.ops_text_dim))
            }
            list.addView(empty)
        }
    }

    private fun renderCall(call: ai.obvious.mcptt.ActiveCall?) {
        if (call == null) {
            callCard?.visibility = View.GONE
            pttButton?.isEnabled = false
            pttLabel?.text = getString(R.string.ptt_no_call)
            return
        }
        callCard?.visibility = View.VISIBLE
        callTitle?.text = call.groupId ?: getString(R.string.private_call)
        callSpeaker?.text = call.speaker?.let { getString(R.string.speaker_now, it) }
            ?: getString(R.string.speaker_none)
        pttButton?.isEnabled = true
    }

    /** The floor-state render — label, color, and live countdown. */
    private fun renderFloor(floor: FloorState) {
        val ctx = context ?: return
        countdownJob?.cancel()
        when (floor) {
            is FloorState.Idle -> {
                pttLabel?.setText(R.string.ptt_ready)
                pttButton?.setText(R.string.ptt_ready)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_surface_higher))
                queueLabel?.visibility = View.GONE
            }
            is FloorState.Requesting -> {
                pttLabel?.setText(R.string.ptt_requesting)
                pttButton?.setText(R.string.ptt_requesting)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_queue))
                queueLabel?.visibility = View.GONE
            }
            is FloorState.Granted -> {
                pttLabel?.setText(R.string.ptt_talking)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_talk))
                startCountdown(floor.burstStartedAtMs, floor.maxBurstMs)
            }
            is FloorState.Queued -> {
                pttLabel?.setText(R.string.ptt_queued)
                pttButton?.setText(R.string.ptt_queued)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_queue))
                queueLabel?.apply {
                    visibility = View.VISIBLE
                    text = getString(R.string.queue_position, floor.position)
                }
            }
            is FloorState.Denied -> {
                pttLabel?.text = getString(R.string.denied_reason, floor.reason)
                pttButton?.setText(R.string.ptt_denied)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_denied))
                queueLabel?.visibility = View.GONE
            }
            is FloorState.Preempted -> {
                pttLabel?.text = getString(R.string.preempted_by, floor.by)
                pttButton?.setText(R.string.ptt_preempted)
                pttButton?.setBackgroundColor(ContextCompat.getColor(ctx, R.color.ops_denied))
                queueLabel?.visibility = View.GONE
            }
        }
    }

    private fun startCountdown(grantedAtMs: Long, maxBurstMs: Long) {
        countdownJob = app.appScope.launch {
            while (true) {
                val remaining = maxBurstMs - (System.currentTimeMillis() - grantedAtMs)
                pttButton?.text = if (remaining > 0) {
                    getString(R.string.burst_remaining, (remaining / 1000) + 1)
                } else {
                    getString(R.string.ptt_ready)
                }
                delay(250)
            }
        }
    }

    private fun endCall() {
        // No member "end" frame in the protocol yet — leaving the call is
        // local until the server exposes call-end to members (dispatcher
        // or initiator ends calls server-side). Documented seam.
        app.controller.activeCall.value?.let { app.controller.pttReleased(it.callId) }
        callCard?.visibility = View.GONE
    }

    override fun onDestroyView() {
        countdownJob?.cancel()
        super.onDestroyView()
    }

    private companion object {
        const val EMERGENCY_HOLD_MS = 1_500L
    }
}
