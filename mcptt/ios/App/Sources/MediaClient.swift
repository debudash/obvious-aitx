// WebRTC media leg — Apple-platform only (stasel/WebRTC binary framework
// via SPM, bound by the XcodeGen project). Flow is client-offer: join()
// creates the peer connection, adds the microphone track (muted until a
// grant), and sends MediaOffer{callId, token, sdp}; the SFU's answer comes
// back through signaling.
//
// Known seam (ios/README.md): the room-scoped media token is not yet
// delivered to clients by any wire frame, so join() is a no-op until
// `roomToken` is set — the moment token delivery lands, no client change
// beyond populating this property is needed.

import Foundation
import WebRTC
import MCPTTCore

final class MediaClient {
    /// The call's room-scoped media token; nil until token delivery exists.
    var roomToken: String?

    private let config: SessionConfig
    private var factory: RTCPeerConnectionFactory?
    private var connection: RTCPeerConnection?
    private var audioTrack: RTCAudioTrack?
    private var activeCallId: String?
    /// Set by the session controller; forwards the offer through signaling.
    var onOffer: ((MediaOfferMessage) -> Void)?

    init(config: SessionConfig) {
        self.config = config
    }

    private func ensureFactory() -> RTCPeerConnectionFactory {
        if let factory { return factory }
        RTCInitializeSSL()
        let encoderFactory = RTCDefaultVideoEncoderFactory()
        let decoderFactory = RTCDefaultVideoDecoderFactory()
        let newFactory = RTCPeerConnectionFactory(
            encoderFactory: encoderFactory, decoderFactory: decoderFactory
        )
        factory = newFactory
        return newFactory
    }

    private func configuration() -> RTCConfiguration {
        let rtcConfig = RTCConfiguration()
        // DTLS-SRTP per hop — the spec's documented TS 33.180 deviation.
        rtcConfig.sdpSemantics = .unifiedPlan
        return rtcConfig
    }

    /// Joins the call's media plane: offer with one audio m-line. The
    /// microphone track starts disabled — media flows only after a grant.
    func join(callId: String) {
        guard activeCallId != callId else { return }
        guard let token = roomToken else {
            // Room-token delivery is not wired server-side yet; the floor
            // UI stays fully functional and media connects when it lands.
            NSLog("mcptt: no room token for \(callId); media leg deferred")
            return
        }
        activeCallId = callId

        let peerFactory = ensureFactory()
        let constraints = RTCMediaConstraints(
            mandatoryConstraints: MediaPlan.audioOnlyConstraints,
            optionalConstraints: nil
        )
        guard let peer = peerFactory.peerConnection(
            with: configuration(), constraints: constraints, delegate: nil
        ) else { return }
        connection = peer

        let source = peerFactory.audioSource(with: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil))
        let track = peerFactory.audioTrack(with: source, trackId: "mcptt-mic")
        track.isEnabled = false // grant-gated; setMicLive flips this
        audioTrack = track
        peer.add(track, streamIds: ["mcptt-\(callId)"])

        peer.offer(for: constraints) { [weak self] sdp, error in
            guard let self, let sdp, error == nil else { return }
            peer.setLocalDescription(sdp) { localError in
                guard localError == nil else { return }
                self.onOffer?(MediaOfferMessage(
                    callId: callId,
                    token: token,
                    sdp: sdp.sdp
                ))
            }
        }
    }

    /// The SFU's answer (or rejection) for our offer.
    func handleAnswer(callId: String, sdp: String?, err: String?) {
        guard callId == activeCallId else { return }
        if let err {
            NSLog("mcptt: media offer rejected for \(callId): \(err)")
            close()
            return
        }
        guard let sdp else { return }
        let remote = RTCSessionDescription(type: .answer, sdp: sdp)
        connection?.setRemoteDescription(remote) { error in
            if let error {
                NSLog("mcptt: setRemoteDescription failed: \(error.localizedDescription)")
            }
        }
    }

    /// Grant/deny gating of the microphone — the only place capture is
    /// enabled, and only on the reducer's .startTransmitting effect.
    func setMicLive(_ live: Bool) {
        audioTrack?.isEnabled = live
    }

    func close() {
        connection?.close()
        connection = nil
        audioTrack = nil
        activeCallId = nil
    }
}
