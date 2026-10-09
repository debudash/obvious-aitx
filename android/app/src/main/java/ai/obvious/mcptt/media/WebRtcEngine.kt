package ai.obvious.mcptt.media

import android.content.Context
import org.webrtc.AudioSource
import org.webrtc.AudioTrack
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.RtpTransceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription

/**
 * WebRTC audio engine over the prebuilt org.webrtc AAR. One peer
 * connection per call: mic send + remote receive through the speaker.
 *
 * The server's SFU is the answerer; the client offers, the server
 * answers, and media flows only for granted floors (server-side gate).
 * Client SDP travels as a MediaAnswer frame — see controller wiring.
 */
class WebRtcEngine(
    private val context: Context,
    private val sendSdp: (callId: String, sdp: String) -> Unit,
) {
    private val factory: PeerConnectionFactory by lazy {
        PeerConnectionFactory.initialize(
            PeerConnectionFactory.InitializationOptions.builder(context.applicationContext)
                .setEnableInternalTracer(false)
                .createInitializationOptions(),
        )
        PeerConnectionFactory.builder().createPeerConnectionFactory()
    }

    private var audioSource: AudioSource? = null
    private var localAudioTrack: AudioTrack? = null
    private var peerConnection: PeerConnection? = null
    private var currentCallId: String? = null

    /** Invoked when the first remote track attaches (route-to-speaker hook). */
    @Volatile
    var onRemoteTrack: (() -> Unit)? = null

    /** Create the peer connection for a call and send our offer. */
    fun joinCall(callId: String) {
        leave()
        currentCallId = callId

        val rtcConfig = PeerConnection.RTCConfiguration(emptyList()).apply {
            sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
            // Loopback/LAN deployments resolve on host candidates; no STUN.
            continualGatheringPolicy = PeerConnection.ContinualGatheringPolicy.GATHER_ONCE
        }

        val observer = object : PeerConnection.Observer {
            override fun onIceCandidate(candidate: IceCandidate) {
                // Non-trickle: candidates ride in the SDP after gathering.
            }

            override fun onConnectionChange(newState: PeerConnection.PeerConnectionState?) {
                // Media-path health surfaces via floor/UI; no extra state here.
            }

            override fun onSignalingChange(state: PeerConnection.SignalingState?) {}
            override fun onIceConnectionChange(state: PeerConnection.IceConnectionState?) {}
            override fun onIceConnectionReceivingChange(receiving: Boolean) {}
            override fun onIceGatheringChange(state: PeerConnection.IceGatheringState?) {}
            override fun onIceCandidatesRemoved(candidates: Array<out IceCandidate>) {}
            override fun onAddStream(stream: MediaStream) {}
            override fun onRemoveStream(stream: MediaStream) {}
            override fun onDataChannel(channel: DataChannel) {}
            override fun onRenegotiationNeeded() {}
            override fun onTrack(transceiver: RtpTransceiver) {
                // Remote audio arrives here; route it to the speaker via
                // the AudioRouter the service owns.
                onRemoteTrack?.invoke()
            }
        }

        val pc = factory.createPeerConnection(rtcConfig, observer) ?: return
        peerConnection = pc

        val constraints = MediaConstraints().apply {
            mandatory.add(MediaConstraints.KeyValuePair("OfferToReceiveAudio", "true"))
            mandatory.add(MediaConstraints.KeyValuePair("OfferToReceiveVideo", "false"))
        }
        val source = factory.createAudioSource(constraints)
        audioSource = source
        val track = factory.createAudioTrack(AUDIO_TRACK_ID, source)
        localAudioTrack = track
        pc.addTrack(track, listOf(AUDIO_STREAM_ID))

        pc.createOffer(
            object : SdpObserver by NoopSdpObserver {
                override fun onCreateSuccess(description: SessionDescription) {
                    pc.setLocalDescription(
                        object : SdpObserver by NoopSdpObserver {
                            override fun onSetSuccess() {
                                sendSdp(callId, description.description)
                            }
                        },
                        description,
                    )
                }
            },
            MediaConstraints(),
        )
    }

    /** Apply the SFU's answer. */
    fun onAnswer(callId: String, sdp: String) {
        if (callId != currentCallId) return
        peerConnection?.setRemoteDescription(
            NoopSdpObserver,
            SessionDescription(SessionDescription.Type.ANSWER, sdp),
        )
    }

    /** Mic enable/disable — the floor grant flips this, not the user. */
    fun setMicEnabled(enabled: Boolean) {
        localAudioTrack?.setEnabled(enabled)
    }

    fun leave() {
        try {
            peerConnection?.close()
        } catch (e: Exception) {
            // PeerConnection.close can throw on a torn-down native stack;
            // a close failure must not wedge the UI or the socket.
        }
        peerConnection = null
        try {
            audioSource?.dispose()
        } catch (e: Exception) {
            // Same native-teardown tolerance as above.
        }
        audioSource = null
        localAudioTrack = null
        currentCallId = null
    }

    private object NoopSdpObserver : SdpObserver {
        override fun onCreateSuccess(sdp: SessionDescription?) {}
        override fun onSetSuccess() {}
        override fun onCreateFailure(error: String?) {}
        override fun onSetFailure(error: String?) {}
    }

    private companion object {
        const val AUDIO_TRACK_ID = "mcptt-audio-send"
        const val AUDIO_STREAM_ID = "mcptt-stream"
    }
}
