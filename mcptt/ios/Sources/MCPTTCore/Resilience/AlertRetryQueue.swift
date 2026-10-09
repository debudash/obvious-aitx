// Outbound emergency-alert durability: an unacknowledged alert is retried
// with capped backoff until the server acknowledges it — the spec's
// "an emergency alert lost to a network drop must retry until acknowledged
// server-side". The queue is pure; the runner injects time and the HTTP call.

import Foundation

/// One alert's durable record.
public struct PendingAlert: Equatable, Sendable, Identifiable {
    public let id: String
    public var groupId: String?
    public var imminentPeril: Bool
    public var lat: Double?
    public var lon: Double?
    public var note: String?
    /// Time of the last submit attempt; drives the next due time.
    public var lastAttemptAt: Date?
    public var attempts: Int

    public init(id: String, groupId: String? = nil, imminentPeril: Bool, lat: Double? = nil, lon: Double? = nil, note: String? = nil) {
        self.id = id
        self.groupId = groupId
        self.imminentPeril = imminentPeril
        self.lat = lat
        self.lon = lon
        self.note = note
        self.lastAttemptAt = nil
        self.attempts = 0
    }
}

public struct AlertRetryQueue: Equatable, Sendable {
    public private(set) var pending: [PendingAlert]

    /// Backoff between submit attempts for one alert: 2s, 4s, 8s, capped at 15s.
    public static let baseDelay: TimeInterval = 2
    public static let maxDelay: TimeInterval = 15

    public init() {
        pending = []
    }

    public var isEmpty: Bool { pending.isEmpty }

    public func alert(id: String) -> PendingAlert? {
        pending.first { $0.id == id }
    }

    /// Whether `alert` should be submitted again at time `now`.
    public func isDue(_ alert: PendingAlert, now: Date) -> Bool {
        guard let last = alert.lastAttemptAt else { return true }
        let delay = Self.delay(forAttempt: alert.attempts)
        return now.timeIntervalSince(last) >= delay
    }

    public static func delay(forAttempt attempt: Int) -> TimeInterval {
        let n = Double(max(1, attempt))
        return min(baseDelay * pow(2.0, n - 1), maxDelay)
    }

    /// Enqueue for the first time. Server-assigned alert ids mean a
    /// client-generated uuid is used as the local key; the server response's
    /// real id supersedes it at acknowledgement.
    public mutating func enqueue(_ alert: PendingAlert) {
        guard !pending.contains(where: { $0.id == alert.id }) else { return }
        pending.append(alert)
    }

    /// Marks a submit attempt made (whether or not it succeeded) — attempts
    /// tick and the schedule advances; a failed HTTP call stays in the queue.
    public mutating func recordAttempt(id: String, at now: Date) {
        guard let idx = pending.firstIndex(where: { $0.id == id }) else { return }
        pending[idx].attempts += 1
        pending[idx].lastAttemptAt = now
    }

    /// The server acknowledged (alert now active server-side, or a later
    /// response confirmed it). Removal is the only exit.
    public mutating func acknowledge(id: String) {
        pending.removeAll { $0.id == id }
    }

    /// Alerts whose next attempt is due at `now`, in queue order.
    public func due(now: Date) -> [PendingAlert] {
        pending.filter { isDue($0, now: now) }
    }
}
