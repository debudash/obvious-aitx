// Reconnect backoff with jitter — the Android contract's
// "auto-reconnect with backoff" mirrored on iOS. Pure schedule computation
// plus a small state holder, both unit-testable without sockets.

import Foundation

/// Computes the delay before reconnect attempt N: base·2^(n-1), capped, with
/// full jitter (random in [0, delay]) — full jitter avoids synchronised
/// reconnect storms from many clients that dropped together.
public struct BackoffSchedule: Sendable {
    public let base: TimeInterval
    public let factor: Double
    public let maxDelay: TimeInterval

    public init(base: TimeInterval = 1.0, factor: Double = 2.0, maxDelay: TimeInterval = 30.0) {
        self.base = base
        self.factor = factor
        self.maxDelay = maxDelay
    }

    public func delay(forAttempt attempt: Int) -> TimeInterval {
        let n = Double(max(1, attempt) - 1)
        let raw = base * pow(factor, n)
        return min(raw, maxDelay)
    }

    /// The deadline the reconnection loop actually sleeps for: jittered.
    public func jitteredDelay(forAttempt attempt: Int, random: Double) -> TimeInterval {
        // random in [0, 1); clamp to defend against rounded inputs.
        let r = min(max(random, 0), 0.999_999)
        return delay(forAttempt: attempt) * r
    }
}

/// Attempt bookkeeping for the reconnection loop. The session controller
/// owns the actual socket; this type only remembers where the schedule is
/// and when the next attempt fires.
public struct ReconnectState: Equatable, Sendable {
    public private(set) var attempt: Int
    public private(set) var connectedEver: Bool

    public init() {
        attempt = 0
        connectedEver = false
    }

    public var nextAttempt: Int { attempt + 1 }

    public mutating func recordConnection() {
        attempt = 0
        connectedEver = true
    }

    public mutating func recordDrop() {
        attempt += 1
    }
}
