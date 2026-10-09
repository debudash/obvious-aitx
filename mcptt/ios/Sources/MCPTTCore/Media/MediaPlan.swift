// Media-plane policy — what the app layer configures on the WebRTC peer
// connection per call, expressed as pure data so the invariants are
// testable without WebRTC bindings.
//
// Invariants carried here (spec: media plane row + boundary invariant):
//   - Audio capture/mic gating only ever turns on after a floor grant whose
//     token matches this call.
//   - The connection is audio-only, recv+send after grant, recv-only until.

import Foundation

/// Direction the peer connection operates in, per the floor state.
public enum MediaDirection: Equatable, Sendable {
    case recvOnly
    case sendRecv
}

public struct MediaPlan: Equatable, Sendable {
    /// Room-scoped token minted per call by the server; the SFU accepts
    /// media under it. Never persisted, never logged.
    public var roomToken: String
    public var callId: String

    public init(roomToken: String, callId: String) {
        self.roomToken = roomToken
        self.callId = callId
    }

    /// The media direction for the current floor state. Listen-only until a
    /// grant arrives — the client cannot transmit itself into a call.
    public func direction(for state: FloorState) -> MediaDirection {
        switch state {
        case .granted:
            return .sendRecv
        case .idle, .requesting, .queued, .denied, .preempted:
            return .recvOnly
        }
    }

    /// Whether the microphone should be live for the current floor state.
    /// The single gate AudioSessionController polls before opening capture.
    public func micLive(for state: FloorState) -> Bool {
        direction(for: state) == .sendRecv
    }

    /// SDP constraints for the offer: audio only, no video, no DTMF.
    public static let audioOnlyConstraints: [String: String] = [
        "OfferToReceiveAudio": "true",
        "OfferToReceiveVideo": "false",
        "DtlsSrtpKeyAgreement": "true",
    ]
}
