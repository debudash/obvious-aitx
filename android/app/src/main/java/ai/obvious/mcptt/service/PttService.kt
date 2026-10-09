package ai.obvious.mcptt.service

import ai.obvious.mcptt.McpttApp
import ai.obvious.mcptt.McpttController
import ai.obvious.mcptt.R
import ai.obvious.mcptt.media.AudioRouter
import ai.obvious.mcptt.media.WebRtcEngine
import ai.obvious.mcptt.protocol.MediaAnswer
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.IBinder

/**
 * Foreground microphone service: owns the WebRTC engine and the audio
 * router for as long as a call is live. Android 9+ requires a foreground
 * service to keep the mic hot in the background; MCPTT users talk with
 * the screen off, so this is load-bearing, not incidental.
 */
class PttService : Service() {

    private lateinit var engine: WebRtcEngine
    private lateinit var router: AudioRouter
    private var currentCallId: String? = null

    override fun onCreate() {
        super.onCreate()
        val app = application as McpttApp
        val controller = app.controller
        router = AudioRouter(this)
        engine = WebRtcEngine(this) { callId, sdp ->
            // Our SDP offer rides as a MediaAnswer frame (server contract).
            controller.sendMediaOffer(MediaAnswer(callId = callId, sdp = sdp))
        }
        engine.onRemoteTrack = { router.routeSpeakerOnly() }
        controller.mediaControl = object : McpttController.MediaControl {
            override fun startTransmit(callId: String) {
                currentCallId = callId
                router.acquireForTransmit()
                engine.setMicEnabled(true)
            }

            override fun stopTransmit(callId: String) {
                engine.setMicEnabled(false)
                router.releaseFromTransmit()
            }

            override fun onMediaAnswer(callId: String, sdp: String) {
                engine.onAnswer(callId, sdp)
            }

            override fun leaveCall() {
                engine.setMicEnabled(false)
                router.releaseFromTransmit()
                engine.leave()
                currentCallId = null
                stopSelf()
            }
        }
        startForeground(NOTIFICATION_ID, buildNotification())
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val callId = intent?.getStringExtra(EXTRA_CALL_ID)
        if (callId != null && callId != currentCallId) {
            currentCallId = callId
            engine.joinCall(callId)
        }
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        (application as McpttApp).controller.mediaControl = null
        engine.leave()
        super.onDestroy()
    }

    private fun buildNotification(): Notification {
        createChannel()
        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.ptt_notification_title))
            .setContentText(getString(R.string.ptt_notification_text))
            .setSmallIcon(android.R.drawable.ic_btn_speak_now)
            .setOngoing(true)
            .build()
    }

    private fun createChannel() {
        val manager = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        val channel = NotificationChannel(CHANNEL_ID, getString(R.string.ptt_channel_name), NotificationManager.IMPORTANCE_LOW)
        manager.createNotificationChannel(channel)
    }

    companion object {
        private const val CHANNEL_ID = "ptt_session"
        private const val NOTIFICATION_ID = 42
        const val EXTRA_CALL_ID = "call_id"

        fun start(context: Context, callId: String) {
            val intent = Intent(context, PttService::class.java).putExtra(EXTRA_CALL_ID, callId)
            context.startForegroundService(intent)
        }
    }
}
