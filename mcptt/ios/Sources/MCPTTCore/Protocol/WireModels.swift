// REST wire models — mirrors of the server's api DTOs (dto.go,
// dispatch_handlers.go sessionDTO, emergency_handlers.go alertDTO) and the
// request/response envelopes the handlers decode and write. Dates are Go
// time.Time JSON (RFC 3339 with optional fractional seconds of any length)
// or the alertDTO's fixed-offset format; both are handled by GoTime.

import Foundation

// MARK: - Server DTOs

/// Mirror of api.userDTO. Role stays a String (validated server-side) so a
/// future role value decodes instead of crashing the roster.
public struct UserDTO: Codable, Equatable, Sendable, Identifiable {
    public let id: String
    public let username: String
    public let displayName: String
    public let role: String
    public let priority: Int
    public let functionalAlias: String?
    public let createdAt: Date

    public enum CodingKeys: String, CodingKey {
        case id, username, displayName, role, priority, functionalAlias, createdAt
    }

    public init(id: String, username: String, displayName: String, role: String, priority: Int, functionalAlias: String?, createdAt: Date) {
        self.id = id
        self.username = username
        self.displayName = displayName
        self.role = role
        self.priority = priority
        self.functionalAlias = functionalAlias
        self.createdAt = createdAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        username = try c.decode(String.self, forKey: .username)
        displayName = try c.decode(String.self, forKey: .displayName)
        role = try c.decode(String.self, forKey: .role)
        priority = try c.decode(Int.self, forKey: .priority)
        functionalAlias = try c.decodeIfPresent(String.self, forKey: .functionalAlias)
        createdAt = try GoTime.decode(try c.decode(String.self, forKey: .createdAt))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(username, forKey: .username)
        try c.encode(displayName, forKey: .displayName)
        try c.encode(role, forKey: .role)
        try c.encode(priority, forKey: .priority)
        try c.encodeIfPresent(functionalAlias, forKey: .functionalAlias)
        try c.encode(GoTime.encode(createdAt), forKey: .createdAt)
    }

    /// Server roles: dispatcher, supervisor, field.
    public var isDispatcher: Bool { role == "dispatcher" }
}

/// Mirror of api.groupDTO.
public struct GroupDTO: Codable, Equatable, Sendable, Identifiable {
    public let id: String
    public let name: String
    public let description: String?
    public let createdBy: String
    public let createdAt: Date

    public enum CodingKeys: String, CodingKey {
        case id, name, description, createdBy, createdAt
    }

    public init(id: String, name: String, description: String?, createdBy: String, createdAt: Date) {
        self.id = id
        self.name = name
        self.description = description
        self.createdBy = createdBy
        self.createdAt = createdAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        description = try c.decodeIfPresent(String.self, forKey: .description)
        createdBy = try c.decode(String.self, forKey: .createdBy)
        createdAt = try GoTime.decode(try c.decode(String.self, forKey: .createdAt))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(name, forKey: .name)
        try c.encodeIfPresent(description, forKey: .description)
        try c.encode(createdBy, forKey: .createdBy)
        try c.encode(GoTime.encode(createdAt), forKey: .createdAt)
    }
}

/// Mirror of api.affiliationDTO.
public struct AffiliationDTO: Codable, Equatable, Sendable, Identifiable {
    public let userId: String
    public let groupId: String
    public let state: String
    public let changedAt: Date

    public var id: String { "\(userId):\(groupId)" }

    public var isAffiliated: Bool { state == AffiliationState.affiliated }

    public init(userId: String, groupId: String, state: String, changedAt: Date) {
        self.userId = userId
        self.groupId = groupId
        self.state = state
        self.changedAt = changedAt
    }

    public enum CodingKeys: String, CodingKey {
        case userId, groupId, state, changedAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        userId = try c.decode(String.self, forKey: .userId)
        groupId = try c.decode(String.self, forKey: .groupId)
        state = try c.decode(String.self, forKey: .state)
        changedAt = try GoTime.decode(try c.decode(String.self, forKey: .changedAt))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(userId, forKey: .userId)
        try c.encode(groupId, forKey: .groupId)
        try c.encode(state, forKey: .state)
        try c.encode(GoTime.encode(changedAt), forKey: .changedAt)
    }
}

/// Mirror of api.alertDTO. `createdAt` is formatted by the server as
/// "2006-01-02T15:04:05Z07:00" (offset form, no fraction).
public struct AlertDTO: Codable, Equatable, Sendable, Identifiable {
    public let id: String
    public let userId: String
    public let callId: String?
    public let kind: String
    public let lat: Double?
    public let lon: Double?
    public let note: String?
    public let status: String
    public let acknowledgedBy: String?
    public let createdAt: Date

    /// Server kinds: "emergency" and "imminent-peril" (store.AlertKind*).
    public var isImminentPeril: Bool { kind == "imminent-peril" }
    public var isActive: Bool { status == "active" }

    public init(
        id: String, userId: String, callId: String?, kind: String,
        lat: Double?, lon: Double?, note: String?,
        status: String, acknowledgedBy: String?, createdAt: Date
    ) {
        self.id = id
        self.userId = userId
        self.callId = callId
        self.kind = kind
        self.lat = lat
        self.lon = lon
        self.note = note
        self.status = status
        self.acknowledgedBy = acknowledgedBy
        self.createdAt = createdAt
    }

    public enum CodingKeys: String, CodingKey {
        case id, userId, callId, kind, lat, lon, note, status, acknowledgedBy, createdAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        userId = try c.decode(String.self, forKey: .userId)
        callId = try c.decodeIfPresent(String.self, forKey: .callId)
        kind = try c.decode(String.self, forKey: .kind)
        lat = try c.decodeIfPresent(Double.self, forKey: .lat)
        lon = try c.decodeIfPresent(Double.self, forKey: .lon)
        note = try c.decodeIfPresent(String.self, forKey: .note)
        status = try c.decode(String.self, forKey: .status)
        acknowledgedBy = try c.decodeIfPresent(String.self, forKey: .acknowledgedBy)
        createdAt = try GoTime.decode(try c.decode(String.self, forKey: .createdAt))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(userId, forKey: .userId)
        try c.encodeIfPresent(callId, forKey: .callId)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(lat, forKey: .lat)
        try c.encodeIfPresent(lon, forKey: .lon)
        try c.encodeIfPresent(note, forKey: .note)
        try c.encode(status, forKey: .status)
        try c.encodeIfPresent(acknowledgedBy, forKey: .acknowledgedBy)
        try c.encode(GoTime.encode(createdAt), forKey: .createdAt)
    }
}

/// Mirror of api.sessionDTO — the client-visible snapshot of one call.
public struct CallSnapshotDTO: Codable, Equatable, Sendable {
    public let callId: String
    public let kind: CallKind
    public let groupId: String?
    public let floorControl: Bool
    public let emergency: Bool
    public let imminentPeril: Bool
    public let participants: [String]
    public let speaker: String?
    public let speakerSince: Date?
    public let queue: [String]?

    public init(
        callId: String, kind: CallKind, groupId: String?, floorControl: Bool,
        emergency: Bool, imminentPeril: Bool, participants: [String],
        speaker: String?, speakerSince: Date?, queue: [String]?
    ) {
        self.callId = callId
        self.kind = kind
        self.groupId = groupId
        self.floorControl = floorControl
        self.emergency = emergency
        self.imminentPeril = imminentPeril
        self.participants = participants
        self.speaker = speaker
        self.speakerSince = speakerSince
        self.queue = queue
    }

    public enum CodingKeys: String, CodingKey {
        case callId, kind, groupId, floorControl, emergency, imminentPeril
        case participants, speaker, speakerSince, queue
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        callId = try c.decode(String.self, forKey: .callId)
        kind = try c.decode(CallKind.self, forKey: .kind)
        groupId = try c.decodeIfPresent(String.self, forKey: .groupId)
        floorControl = try c.decode(Bool.self, forKey: .floorControl)
        emergency = try c.decode(Bool.self, forKey: .emergency)
        imminentPeril = try c.decode(Bool.self, forKey: .imminentPeril)
        participants = try c.decode([String].self, forKey: .participants)
        speaker = try c.decodeIfPresent(String.self, forKey: .speaker)
        if let raw = try c.decodeIfPresent(String.self, forKey: .speakerSince) {
            speakerSince = try GoTime.decode(raw)
        } else {
            speakerSince = nil
        }
        queue = try c.decodeIfPresent([String].self, forKey: .queue)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(callId, forKey: .callId)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(groupId, forKey: .groupId)
        try c.encode(floorControl, forKey: .floorControl)
        try c.encode(emergency, forKey: .emergency)
        try c.encode(imminentPeril, forKey: .imminentPeril)
        try c.encode(participants, forKey: .participants)
        try c.encodeIfPresent(speaker, forKey: .speaker)
        try c.encodeIfPresent(speakerSince.map(GoTime.encode), forKey: .speakerSince)
        try c.encodeIfPresent(queue, forKey: .queue)
    }
}

/// Mirror of floor.FloorDecision's JSON — rides the emergency-call REST
/// response (`decision`) and dispatcher action responses. A granted
/// decision's `token` is the floor token; the room-scoped media token for
/// MediaOffer currently reaches the client only through this shape
/// (documented delivery gap in ios/README.md).
public struct FloorDecisionDTO: Codable, Equatable, Sendable {
    public let userId: String
    public let outcome: String
    public let priority: Int
    public let emergency: Bool?
    public let token: String?
    public let queuePosition: Int?
    public let reason: String?
    public let preemptedUserId: String?

    /// Server outcomes: "granted", "queued", "denied" (floor.DecisionOutcome).
    public var isGranted: Bool { outcome == "granted" }

    public init(
        userId: String, outcome: String, priority: Int, emergency: Bool?,
        token: String?, queuePosition: Int?, reason: String?, preemptedUserId: String?
    ) {
        self.userId = userId
        self.outcome = outcome
        self.priority = priority
        self.emergency = emergency
        self.token = token
        self.queuePosition = queuePosition
        self.reason = reason
        self.preemptedUserId = preemptedUserId
    }
}

// MARK: - REST requests / responses

public struct LoginRequest: Codable, Equatable, Sendable {
    public let username: String
    public let password: String

    public init(username: String, password: String) {
        self.username = username
        self.password = password
    }
}

/// Response of POST /api/auth/login (and register): {"user": ..., "token": ...}
public struct LoginResponse: Codable, Equatable, Sendable {
    public let user: UserDTO
    public let token: String
}

/// Body of POST /api/calls/emergency and POST /api/emergency/alerts
/// (server: emergencyCallRequest). Exactly one of callId / groupId for the
/// call path; both empty for the voiceless alert path.
public struct EmergencyCallRequest: Codable, Equatable, Sendable {
    public var callId: String?
    public var groupId: String?
    public var imminentPeril: Bool
    public var lat: Double?
    public var lon: Double?
    public var note: String?

    public init(callId: String? = nil, groupId: String? = nil, imminentPeril: Bool, lat: Double? = nil, lon: Double? = nil, note: String? = nil) {
        self.callId = callId
        self.groupId = groupId
        self.imminentPeril = imminentPeril
        self.lat = lat
        self.lon = lon
        self.note = note
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(callId, forKey: .callId)
        try c.encodeIfPresent(groupId, forKey: .groupId)
        try c.encode(imminentPeril, forKey: .imminentPeril)
        try c.encodeIfPresent(lat, forKey: .lat)
        try c.encodeIfPresent(lon, forKey: .lon)
        try c.encodeIfPresent(note, forKey: .note)
    }
}

/// Response of POST /api/calls/emergency: {"call": ..., "decision": ..., "alert": ...}
public struct EmergencyCallResponse: Codable, Equatable, Sendable {
    public let call: CallSnapshotDTO
    public let decision: FloorDecisionDTO
    public let alert: AlertDTO
}

/// The server's error envelope: {"error": "..."} plus registration's
/// {"problems": [...]} list.
public struct APIErrorBody: Codable, Equatable, Sendable {
    public let error: String
    public let problems: [String]?
}

// MARK: - Go time JSON

/// Go's time.Time marshals as RFC 3339 with an optional fractional part of
/// up to nine digits ("2026-10-09T20:42:25.123456789Z"); alertDTO stamps a
/// fixed offset form. ISO8601DateFormatter needs exactly one format, so the
/// parser below handles the variants explicitly.
public enum GoTime {
    /// Parses an RFC 3339 timestamp with optional fraction (1–9 digits) and
    /// Z or ±HH:MM offset. Throws DecodingError on anything else.
    public static func decode(_ s: String) throws -> Date {
        let chars = Array(s.utf8)
        guard chars.count >= 20,
              chars[4] == UInt8(ascii: "-"), chars[7] == UInt8(ascii: "-"),
              chars[10] == UInt8(ascii: "T"), chars[13] == UInt8(ascii: ":"),
              chars[16] == UInt8(ascii: ":")
        else {
            throw goTimeError(s)
        }
        func int(_ from: Int, _ length: Int) throws -> Int {
            var value = 0
            for i in from..<(from + length) {
                let b = chars[i]
                guard b >= UInt8(ascii: "0"), b <= UInt8(ascii: "9") else {
                    throw goTimeError(s)
                }
                value = value * 10 + Int(b - UInt8(ascii: "0"))
            }
            return value
        }

        let year = try int(0, 4)
        let month = try int(5, 2)
        let day = try int(8, 2)
        let hour = try int(11, 2)
        let minute = try int(14, 2)
        let second = try int(17, 2)

        var fractionSeconds = 0.0
        var index = 19
        if index < chars.count && chars[index] == UInt8(ascii: ".") {
            index += 1
            var fractionDigits = ""
            while index < chars.count, chars[index] >= UInt8(ascii: "0"), chars[index] <= UInt8(ascii: "9") {
                fractionDigits.append(String(Character(UnicodeScalar(chars[index]))))
                index += 1
            }
            guard !fractionDigits.isEmpty else { throw goTimeError(s) }
            fractionSeconds = Double("0." + fractionDigits) ?? 0
        }

        guard index < chars.count else { throw goTimeError(s) }
        var offsetSeconds = 0
        switch chars[index] {
        case UInt8(ascii: "Z"), UInt8(ascii: "z"):
            index += 1
        case UInt8(ascii: "+"), UInt8(ascii: "-"):
            let sign = chars[index] == UInt8(ascii: "+") ? 1 : -1
            guard chars.count >= index + 6 else { throw goTimeError(s) }
            let oh = try int(index + 1, 2)
            guard chars[index + 3] == UInt8(ascii: ":") else { throw goTimeError(s) }
            let om = try int(index + 4, 2)
            offsetSeconds = sign * (oh * 3600 + om * 60)
            index += 6
        default:
            throw goTimeError(s)
        }
        guard index == chars.count else { throw goTimeError(s) }

        var components = DateComponents()
        components.year = year
        components.month = month
        components.day = day
        components.hour = hour
        components.minute = minute
        components.second = second
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0) ?? .current
        guard let base = calendar.date(from: components) else { throw goTimeError(s) }
        return base.addingTimeInterval(fractionSeconds - Double(offsetSeconds))
    }

    /// Encodes as the Z form with millisecond fraction — a lossless-enough
    /// canonical form for a client that never re-stamps server DTOs.
    public static func encode(_ date: Date) -> String {
        let millis = Int((date.timeIntervalSince1970 * 1000).rounded())
        let seconds = millis / 1000
        let ms = millis % 1000
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        var out = f.string(from: Date(timeIntervalSince1970: TimeInterval(seconds)))
        if ms != 0 {
            out = out.replacingOccurrences(of: "Z", with: String(format: ".%03dZ", ms))
        }
        return out
    }

    private static func goTimeError(_ s: String) -> DecodingError {
        DecodingError.dataCorrupted(.init(
            codingPath: [], debugDescription: "unsupported Go time JSON: \(s)"
        ))
    }
}
