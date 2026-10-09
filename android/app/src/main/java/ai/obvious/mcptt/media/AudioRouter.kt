package ai.obvious.mcptt.media

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager

/**
 * Voice-call audio policy: transient-exclusive focus while transmitting,
 * speakerphone mode for group listening (spec: "audio focus ownership
 * while transmitting; speakerphone mode").
 */
class AudioRouter(context: Context) {
    private val audioManager = context.getSystemService(Context.AUDIO_SERVICE) as AudioManager
    private val focusRequest = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
        .setAudioAttributes(
            AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                .build(),
        )
        .build()

    fun acquireForTransmit() {
        audioManager.requestAudioFocus(focusRequest)
        audioManager.mode = AudioManager.MODE_IN_COMMUNICATION
        audioManager.isSpeakerphoneOn = true
    }

    fun releaseFromTransmit() {
        audioManager.isSpeakerphoneOn = false
        audioManager.mode = AudioManager.MODE_NORMAL
        audioManager.abandonAudioFocusRequest(focusRequest)
    }

    /** Receive-only listening keeps the speaker routed but drops focus claims. */
    fun routeSpeakerOnly() {
        audioManager.mode = AudioManager.MODE_IN_COMMUNICATION
        audioManager.isSpeakerphoneOn = true
    }
}
