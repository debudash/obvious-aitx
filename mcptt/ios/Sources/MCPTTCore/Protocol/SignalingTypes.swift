// Wire message types — the Swift mirror of the server's pinned contract in
// mcptt/server/internal/protocol/protocol.go. Field names and JSON keys match
// the Go structs exactly; the test suite pins every key against golden JSON
// so a server-side drift fails loudly here instead of silently at runtime.
//
// Signaling is WebSocket JSON with 3GPP TS 24.379 floor-control semantics —
// the spec's documented wire-protocol deviation from SIP.

import Foundation

/// The three call entry shapes (TS 23.379 pre-arranged group, one-to-one,
/// dispatcher announcement). Wire values match the server's CallKind strings.
public enum CallKind: String, Codable, Equatable, Sendable, CaseIterable {
    case group
    case privateCall = "private"
    case broadcast
}

/// Values carried by PresenceUpdate.state.
public enum PresenceState {
    public static let online = "online"
    public static let offline = "offline"
}

/// Values carried by AffiliationChanged.state (TS 23.280 affiliation).
public enum AffiliationState {
    public static let affiliated = "affiliated"
    public static let deaffiliated = "deaffiliated"
}

// MARK: - Floor control

/// client → server: ask for the talk floor. Priority is the sender's own
/// ladder level; the server re-reads it from the store and never trusts the
/// claim — the field exists for 3GPP shape parity.
public struct FloorRequestMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorRequest"

    public var type: String = FloorRequestMessage.messageType
    public var callId: String
    public var userId: String
    public var priority: Int
    public var emergency: Bool

    public init(callId: String, userId: String, priority: Int, emergency: Bool) {
        self.callId = callId
        self.userId = userId
        self.priority = priority
        self.emergency = emergency
    }
}

/// client → server after releasing PTT. Doubles as a queue-cancellation:
/// the server treats a release from a queued requester as "leave the queue".
public struct FloorReleasedMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorReleased"

    public var type: String = FloorReleasedMessage.messageType
    public var callId: String
    public var userId: String

    public init(callId: String, userId: String) {
        self.callId = callId
        self.userId = userId
    }
}

/// server → all participants: arbitration result. `queue` is the ordered
/// waiting list in priority order; it arrives as null on fan-outs that carry
/// no queue (e.g. a revoke with no successor), so it decodes to [].
public struct FloorGrantedMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorGranted"

    public var type: String = FloorGrantedMessage.messageType
    public var callId: String
    public var userId: String
    public var queue: [String]?

    public init(callId: String, userId: String, queue: [String]?) {
        self.callId = callId
        self.userId = userId
        self.queue = queue
    }
}

/// server → requester: rejected, with reason ("busy", "not-affiliated",
/// "listen-only", "net-control", ...) and queue position when the request
/// was spillover. Also the wire consequence of a dispatcher revoking the
/// current talker — a revoked holder receives a denial, not a special type.
public struct FloorDeniedMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorDenied"

    public var type: String = FloorDeniedMessage.messageType
    public var callId: String
    public var userId: String
    public var reason: String
    public var queuePosition: Int

    public init(callId: String, userId: String, reason: String, queuePosition: Int) {
        self.callId = callId
        self.userId = userId
        self.reason = reason
        self.queuePosition = queuePosition
    }

    public enum CodingKeys: String, CodingKey {
        case type, callId, userId, reason, queuePosition
    }

    /// The server marks reason and queuePosition omitempty — a denial can
    /// arrive with neither key; absent decodes to the zero values rather
    /// than failing the frame.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        type = try c.decode(String.self, forKey: .type)
        callId = try c.decode(String.self, forKey: .callId)
        userId = try c.decode(String.self, forKey: .userId)
        reason = try c.decodeIfPresent(String.self, forKey: .reason) ?? ""
        queuePosition = try c.decodeIfPresent(Int.self, forKey: .queuePosition) ?? 0
    }
}

/// server → all: a higher-priority floor took the call mid-burst (emergency
/// or dispatcher net control).
public struct FloorPreemptedMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorPreempted"

    public var type: String = FloorPreemptedMessage.messageType
    public var callId: String
    public var by: String
    public var emergency: Bool

    public init(callId: String, by: String, emergency: Bool) {
        self.callId = callId
        self.by = by
        self.emergency = emergency
    }
}

/// dispatcher → server: strip the current talker's floor. A field client
/// never sends this (net control is the console's P10 surface) but decodes
/// the type so unknown senders stay forward-compatible.
public struct FloorRevokeMessage: Codable, Equatable, Sendable {
    public static let messageType = "FloorRevoke"

    public var type: String = FloorRevokeMessage.messageType
    public var callId: String
    public var by: String
    public var reason: String

    public init(callId: String, by: String, reason: String) {
        self.callId = callId
        self.by = by
        self.reason = reason
    }
}

// MARK: - Call lifecycle

/// client → server: open a call; server → all: the call announcement (the
/// server reuses the type for its fan-out with the server-minted callId).
///
/// Known contract gap (documented in ios/README.md): for kind == .privateCall
/// the frame carries no callee field, so a private call's target is not
/// expressible on the wire yet. The client decodes an optional `calleeId`
/// when the server begins sending it and otherwise surfaces the call as
/// ringing-without-target.
public struct CallStartMessage: Codable, Equatable, Sendable {
    public static let messageType = "CallStart"

    public var type: String = CallStartMessage.messageType
    public var callId: String
    public var groupId: String?
    public var kind: CallKind
    public var initiatorId: String
    /// Not in the pinned server contract yet; decodes when it appears.
    public var calleeId: String?

    public init(callId: String, groupId: String?, kind: CallKind, initiatorId: String, calleeId: String? = nil) {
        self.callId = callId
        self.groupId = groupId
        self.kind = kind
        self.initiatorId = initiatorId
        self.calleeId = calleeId
    }

    public enum CodingKeys: String, CodingKey {
        case type, callId, groupId, kind, initiatorId
        case calleeId
    }
}

/// server → participants: a party joined (late entry).
public struct CallJoinedMessage: Codable, Equatable, Sendable {
    public static let messageType = "CallJoined"

    public var type: String = CallJoinedMessage.messageType
    public var callId: String
    public var userId: String

    public init(callId: String, userId: String) {
        self.callId = callId
        self.userId = userId
    }
}

/// server → participants: call torn down.
public struct CallEndedMessage: Codable, Equatable, Sendable {
    public static let messageType = "CallEnded"

    public var type: String = CallEndedMessage.messageType
    public var callId: String
    public var by: String

    public init(callId: String, by: String) {
        self.callId = callId
        self.by = by
    }
}

/// server → all: a dispatcher stripped one party from a live call; their
/// media leg is dropped server-side (zero further packets).
public struct ParticipantRemovedMessage: Codable, Equatable, Sendable {
    public static let messageType = "ParticipantRemoved"

    public var type: String = ParticipantRemovedMessage.messageType
    public var callId: String
    public var userId: String
    public var by: String

    public init(callId: String, userId: String, by: String) {
        self.callId = callId
        self.userId = userId
        self.by = by
    }
}

// MARK: - Emergency

/// client → server (WSS path) and server → all (fan-out): one-tap alert
/// (voiceless) or the alert that accompanies an emergency call. Location is
/// client-reported WGS-84 and omitted when the device has no fix.
///
/// The REST paths (POST /api/calls/emergency, POST /api/emergency/alerts)
/// are the live server surface today; the WSS ingest of this frame is not
/// wired yet (documented gap). The client's alert outbox uses REST and keeps
/// this type for the WSS channel's client-minted-alertId idempotency once
/// ingest lands.
public struct EmergencyAlertMessage: Codable, Equatable, Sendable {
    public static let messageType = "EmergencyAlert"

    public var type: String = EmergencyAlertMessage.messageType
    public var alertId: String
    public var userId: String
    public var callId: String?
    public var lat: Double?
    public var lon: Double?
    public var emergency: Bool
    public var imminentPeril: Bool?
    public var note: String?

    public init(
        alertId: String, userId: String, callId: String? = nil,
        lat: Double? = nil, lon: Double? = nil,
        emergency: Bool, imminentPeril: Bool = false, note: String? = nil
    ) {
        self.alertId = alertId
        self.userId = userId
        self.callId = callId
        self.lat = lat
        self.lon = lon
        self.emergency = emergency
        self.imminentPeril = imminentPeril
        self.note = note
    }

    public enum CodingKeys: String, CodingKey {
        case type, alertId, userId, callId, lat, lon, emergency, imminentPeril, note
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(type, forKey: .type)
        try c.encode(alertId, forKey: .alertId)
        try c.encode(userId, forKey: .userId)
        try c.encodeIfPresent(callId, forKey: .callId)
        try c.encodeIfPresent(lat, forKey: .lat)
        try c.encodeIfPresent(lon, forKey: .lon)
        try c.encode(emergency, forKey: .emergency)
        try c.encodeIfPresent(imminentPeril, forKey: .imminentPeril)
        try c.encodeIfPresent(note, forKey: .note)
    }
}

/// server → all: a dispatcher acknowledged an alert; the emergency rail
/// clears it into the archive.
public struct EmergencyAlertAckMessage: Codable, Equatable, Sendable {
    public static let messageType = "EmergencyAlertAck"

    public var type: String = EmergencyAlertAckMessage.messageType
    public var alertId: String
    public var acknowledgedBy: String

    public init(alertId: String, acknowledgedBy: String) {
        self.alertId = alertId
        self.acknowledgedBy = acknowledgedBy
    }
}

// MARK: - Foundation-live fan-out

/// server → all connected clients: a user connected to or dropped from /ws.
/// `at` is unix millis.
public struct PresenceUpdateMessage: Codable, Equatable, Sendable {
    public static let messageType = "PresenceUpdate"

    public var type: String = PresenceUpdateMessage.messageType
    public var userId: String
    public var state: String
    public var at: Int64

    public init(userId: String, state: String, at: Int64) {
        self.userId = userId
        self.state = state
        self.at = at
    }
}

/// server → all connected clients: a user's group affiliation changed,
/// dispatched within the same round trip as the REST mutation that caused it.
public struct AffiliationChangedMessage: Codable, Equatable, Sendable {
    public static let messageType = "AffiliationChanged"

    public var type: String = AffiliationChangedMessage.messageType
    public var userId: String
    public var groupId: String
    public var state: String
    public var at: Int64

    public init(userId: String, groupId: String, state: String, at: Int64) {
        self.userId = userId
        self.groupId = groupId
        self.state = state
        self.at = at
    }
}

// MARK: - Media signaling

/// client → server: WebRTC offer for a call's media plane, carrying the
/// room-scoped media token minted for this call and user. The offer contains
/// exactly one sendrecv audio m-line (the client's microphone).
public struct MediaOfferMessage: Codable, Equatable, Sendable {
    public static let messageType = "MediaOffer"

    public var type: String = MediaOfferMessage.messageType
    public var callId: String
    public var token: String
    public var sdp: String

    public init(callId: String, token: String, sdp: String) {
        self.callId = callId
        self.token = token
        self.sdp = sdp
    }
}

/// server → client: the SFU's answer SDP, or an empty SDP with err set when
/// the offer was rejected (bad or mismatched room token, unparsable SDP,
/// call ended).
public struct MediaAnswerMessage: Codable, Equatable, Sendable {
    public static let messageType = "MediaAnswer"

    public var type: String = MediaAnswerMessage.messageType
    public var callId: String
    public var sdp: String?
    public var err: String?

    public init(callId: String, sdp: String?, err: String?) {
        self.callId = callId
        self.sdp = sdp
        self.err = err
    }
}
