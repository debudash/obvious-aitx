// Active-call bookkeeping rendered from the server's fan-out frames: which
// calls exist, who speaks, who waits, who is present — plus the missed-call
// list the spec requires for declined or unanswered private calls.
//
// Pure value type: mutations happen through `mutating` reduces, no I/O.

import Foundation

/// One live call as the field client renders it.
public struct ActiveCall: Equatable, Sendable, Identifiable {
    public let id: String
    public var kind: CallKind
    public var groupId: String?
    public var initiatorId: String
    /// Set when the announcement names the callee (not yet in the pinned
    /// contract — see CallStartMessage.calleeId).
    public var calleeId: String?
    public var participants: [String]
    public var speaker: String?
    public var queue: [String]
    public var emergency: Bool
    public var floorControl: Bool
}

/// A private call that reached its end without the local user joining —
/// declined locally, or ended while never accepted.
public struct MissedCall: Equatable, Sendable, Identifiable {
    public let id: String
    public let fromUserId: String
    public let at: Date
    public let kind: MissedCallKind

    public enum MissedCallKind: String, Sendable {
        case declined
        case unanswered
    }
}

/// The client-side call roster. `joinedCallIds` tracks which calls this user
/// has actually joined — the distinction the missed-call rule needs.
public struct CallStore: Equatable, Sendable {
    public private(set) var calls: [ActiveCall]
    public private(set) var missedCalls: [MissedCall]
    public private(set) var joinedCallIds: Set<String>

    /// Caps the missed-call list; older entries fall off the tail.
    public static let missedCallLimit = 50

    public init() {
        calls = []
        missedCalls = []
        joinedCallIds = []
    }

    /// The call the PTT panel acts on: a call this user joined, else the
    /// first visible call.
    public var activeCall: ActiveCall? {
        calls.first { joinedCallIds.contains($0.id) } ?? calls.first
    }

    public func call(id: String) -> ActiveCall? {
        calls.first { $0.id == id }
    }

    // MARK: Wire reduces

    /// Applies one inbound frame. `now` stamps missed-call entries; `myUserId`
    /// decides whether a private call's end counts as missed for this user.
    public mutating func reduce(_ message: InboundMessage, myUserId: String, now: Date) {
        switch message {
        case .callStarted(let start):
            // The server's announcement is authoritative for call identity.
            guard !calls.contains(where: { $0.id == start.callId }) else { return }
            calls.append(ActiveCall(
                id: start.callId,
                kind: start.kind,
                groupId: start.groupId,
                initiatorId: start.initiatorId,
                calleeId: start.calleeId,
                participants: [start.initiatorId],
                speaker: nil,
                queue: [],
                emergency: false,
                floorControl: start.kind != .privateCall
            ))

        case .callJoined(let join):
            guard var call = call(id: join.callId) else { return }
            if !call.participants.contains(join.userId) {
                call.participants.append(join.userId)
            }
            replace(call)

        case .floorGranted(let grant):
            guard var call = call(id: grant.callId) else { return }
            call.speaker = grant.userId
            call.queue = grant.queue ?? []
            replace(call)

        case .floorPreempted(let preempt):
            guard var call = call(id: preempt.callId) else { return }
            call.speaker = preempt.by
            call.emergency = call.emergency || preempt.emergency
            replace(call)

        case .participantRemoved(let removed):
            guard var call = call(id: removed.callId) else { return }
            call.participants.removeAll { $0 == removed.userId }
            call.queue.removeAll { $0 == removed.userId }
            if call.speaker == removed.userId {
                call.speaker = nil
            }
            if removed.userId == myUserId {
                joinedCallIds.remove(removed.callId)
                calls.removeAll { $0.id == removed.callId }
            } else {
                replace(call)
            }

        case .callEnded(let ended):
            if let call = call(id: ended.callId) {
                recordMissedIfAppropriate(call: call, endedBy: ended.by, myUserId: myUserId, now: now)
            }
            joinedCallIds.remove(ended.callId)
            calls.removeAll { $0.id == ended.callId }

        case .emergencyAlert(let alert):
            // An alert naming a call marks that call emergency on the roster.
            guard let callId = alert.callId, var call = call(id: callId) else { return }
            call.emergency = true
            replace(call)

        default:
            break
        }
    }

    // MARK: Local actions

    /// This user accepted a call — late entry or first join.
    public mutating func markJoined(callId: String) {
        joinedCallIds.insert(callId)
    }

    /// This user declined a ringing private call: the spec's missed-call
    /// fallback fires immediately.
    public mutating func markDeclined(callId: String, myUserId: String, now: Date) {
        joinedCallIds.remove(callId)
        guard let call = call(id: callId), call.kind == .privateCall else {
            calls.removeAll { $0.id == callId }
            return
        }
        recordMissed(call: call, kind: .declined, at: now)
        calls.removeAll { $0.id == callId }
    }

    /// This user left the call voluntarily — no missed-call entry.
    public mutating func leave(callId: String) {
        joinedCallIds.remove(callId)
        calls.removeAll { $0.id == callId }
    }

    /// Removes one missed-call entry (viewed / cleared).
    public mutating func clearMissedCall(id: String) {
        missedCalls.removeAll { $0.id == id }
    }

    // MARK: Helpers

    private mutating func recordMissedIfAppropriate(call: ActiveCall, endedBy: String, myUserId: String, now: Date) {
        // Missed-call entries apply to private calls only, and only when
        // this user never joined and did not end the call themselves.
        guard call.kind == .privateCall else { return }
        guard !joinedCallIds.contains(call.id) else { return }
        guard endedBy != myUserId else { return }
        // The callee is known only when the announcement carried it.
        guard let callee = call.calleeId, callee == myUserId else { return }
        recordMissed(call: call, kind: .unanswered, at: now)
    }

    private mutating func recordMissed(call: ActiveCall, kind: MissedCall.MissedCallKind, at: Date) {
        guard !missedCalls.contains(where: { $0.id == call.id }) else { return }
        missedCalls.insert(
            MissedCall(id: call.id, fromUserId: call.initiatorId, at: at, kind: kind),
            at: 0
        )
        if missedCalls.count > CallStore.missedCallLimit {
            missedCalls.removeLast(missedCalls.count - CallStore.missedCallLimit)
        }
    }

    private mutating func replace(_ call: ActiveCall) {
        guard let idx = calls.firstIndex(where: { $0.id == call.id }) else { return }
        calls[idx] = call
    }
}
