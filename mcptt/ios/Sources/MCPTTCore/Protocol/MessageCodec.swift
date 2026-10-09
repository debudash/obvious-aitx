// The codec routes one WSS frame to its typed message and encodes outbound
// frames. Decoding is strictly forward-compatible: an unknown type string
// decodes to .unknown rather than failing, mirroring the server's own
// "unknown, malformed, or unexpected type is dropped" behavior — but the
// failure mode is visible to the transport layer, never silent.

import Foundation

/// Every way a frame can fail to decode. The transport logs these; the
/// connection survives.
public enum MessageCodecError: Error, Equatable, Sendable {
    case malformedEnvelope
    case unknownType(String)
    case malformedPayload(type: String)
}

/// The inbound frame union — everything the server can fan out. One case per
/// pinned wire type plus `.unknown` for forward compatibility.
public enum InboundMessage: Equatable, Sendable {
    case floorGranted(FloorGrantedMessage)
    case floorDenied(FloorDeniedMessage)
    case floorPreempted(FloorPreemptedMessage)
    case floorReleased(FloorReleasedMessage)
    case floorRevoke(FloorRevokeMessage)
    case floorRequest(FloorRequestMessage) // echo of another party's request, when present
    case callStarted(CallStartMessage)
    case callJoined(CallJoinedMessage)
    case callEnded(CallEndedMessage)
    case participantRemoved(ParticipantRemovedMessage)
    case emergencyAlert(EmergencyAlertMessage)
    case emergencyAlertAck(EmergencyAlertAckMessage)
    case presenceUpdate(PresenceUpdateMessage)
    case affiliationChanged(AffiliationChangedMessage)
    case mediaAnswer(MediaAnswerMessage)
    case unknown(type: String)
}

public enum MessageCodec {
    /// Decodes one wire frame. Unknown-but-well-formed types return
    /// `.success(.unknown)`; malformed envelopes and malformed payloads of a
    /// known type return `.failure` — never a silent drop.
    public static func decode(_ data: Data) -> Result<InboundMessage, MessageCodecError> {
        guard let envelope = try? JSONDecoder().decode(Envelope.self, from: data) else {
            return .failure(.malformedEnvelope)
        }
        switch envelope.type {
        case FloorGrantedMessage.messageType:
            return map(data, envelope.type) { .floorGranted($0) }
        case FloorDeniedMessage.messageType:
            return map(data, envelope.type) { .floorDenied($0) }
        case FloorPreemptedMessage.messageType:
            return map(data, envelope.type) { .floorPreempted($0) }
        case FloorReleasedMessage.messageType:
            return map(data, envelope.type) { .floorReleased($0) }
        case FloorRevokeMessage.messageType:
            return map(data, envelope.type) { .floorRevoke($0) }
        case FloorRequestMessage.messageType:
            return map(data, envelope.type) { .floorRequest($0) }
        case CallStartMessage.messageType:
            return map(data, envelope.type) { .callStarted($0) }
        case CallJoinedMessage.messageType:
            return map(data, envelope.type) { .callJoined($0) }
        case CallEndedMessage.messageType:
            return map(data, envelope.type) { .callEnded($0) }
        case ParticipantRemovedMessage.messageType:
            return map(data, envelope.type) { .participantRemoved($0) }
        case EmergencyAlertMessage.messageType:
            return map(data, envelope.type) { .emergencyAlert($0) }
        case EmergencyAlertAckMessage.messageType:
            return map(data, envelope.type) { .emergencyAlertAck($0) }
        case PresenceUpdateMessage.messageType:
            return map(data, envelope.type) { .presenceUpdate($0) }
        case AffiliationChangedMessage.messageType:
            return map(data, envelope.type) { .affiliationChanged($0) }
        case MediaAnswerMessage.messageType:
            return map(data, envelope.type) { .mediaAnswer($0) }
        default:
            return .success(.unknown(type: envelope.type))
        }
    }

    private static func map<T: Decodable>(
        _ data: Data, _ type: String, _ wrap: (T) -> InboundMessage
    ) -> Result<InboundMessage, MessageCodecError> {
        let decoder = JSONDecoder()
        do {
            return .success(wrap(try decoder.decode(T.self, from: data)))
        } catch {
            return .failure(.malformedPayload(type: type))
        }
    }

    // MARK: Outbound encoding

    public static func encode(_ message: FloorRequestMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }

    public static func encode(_ message: FloorReleasedMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }

    public static func encode(_ message: CallStartMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }

    public static func encode(_ message: EmergencyAlertMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }

    public static func encode(_ message: MediaOfferMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }

    public static func encode(_ message: FloorRevokeMessage) throws -> Data {
        try JSONEncoder().encode(message)
    }
}

/// The outer frame of every WSS message; each typed message carries its own
/// `type` value (the server's protocol.Envelope).
private struct Envelope: Decodable {
    let type: String
}
