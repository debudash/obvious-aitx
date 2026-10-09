// CallStore reduces: group lifecycle, late entry, missed calls.

import XCTest
@testable import MCPTTCore

final class CallStoreTests: XCTestCase {
    private let me = "user_me"

    private func groupStart(callId: String, groupId: String) -> InboundMessage {
        .callStarted(CallStartMessage(
            callId: callId, groupId: groupId, kind: .group,
            initiatorId: "user_a", calleeId: nil
        ))
    }

    private func privateStart(callId: String, initiator: String, callee: String) -> InboundMessage {
        .callStarted(CallStartMessage(
            callId: callId, groupId: nil, kind: .privateCall,
            initiatorId: initiator, calleeId: callee
        ))
    }

    func testGroupCallStartAddsCall() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        XCTAssertEqual(store.calls.count, 1)
        XCTAssertEqual(store.activeCall?.id, "c1")
        XCTAssertFalse(store.activeCall?.emergency ?? true)
    }

    func testJoinAddsParticipantAndMarksJoined() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.reduce(.callJoined(CallJoinedMessage(callId: "c1", userId: me)), myUserId: me, now: Date())
        XCTAssertEqual(store.call(id: "c1")?.participants, ["user_a", me])
        store.markJoined(callId: "c1")
        XCTAssertTrue(store.joinedCallIds.contains("c1"))
    }

    func testFloorGrantedUpdatesSpeakerAndQueue() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.reduce(
            .floorGranted(FloorGrantedMessage(callId: "c1", userId: "user_b", queue: [me])),
            myUserId: me, now: Date()
        )
        XCTAssertEqual(store.call(id: "c1")?.speaker, "user_b")
        XCTAssertEqual(store.call(id: "c1")?.queue, [me])
    }

    func testPreemptMarksEmergencyAndSwapsSpeaker() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.reduce(
            .floorGranted(FloorGrantedMessage(callId: "c1", userId: "user_b", queue: [])),
            myUserId: me, now: Date()
        )
        store.reduce(
            .floorPreempted(FloorPreemptedMessage(
                callId: "c1", by: "user_c", emergency: true
            )),
            myUserId: me, now: Date()
        )
        XCTAssertEqual(store.call(id: "c1")?.speaker, "user_c")
        XCTAssertTrue(store.call(id: "c1")?.emergency ?? false)
    }

    func testRemovedParticipantLeavesCall() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.reduce(.callJoined(CallJoinedMessage(callId: "c1", userId: "user_b")), myUserId: me, now: Date())
        store.reduce(
            .participantRemoved(ParticipantRemovedMessage(callId: "c1", userId: "user_b", by: "dispatcher")),
            myUserId: me, now: Date()
        )
        XCTAssertEqual(store.call(id: "c1")?.participants, ["user_a"])
    }

    func testCallEndedRemovesCall() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.markJoined(callId: "c1")
        store.reduce(.callEnded(CallEndedMessage(callId: "c1", by: "user_a")), myUserId: me, now: Date())
        XCTAssertNil(store.activeCall)
    }

    // MARK: Missed calls

    func testUnansweredPrivateCallBecomesMissedForCallee() {
        var store = CallStore()
        store.reduce(privateStart(callId: "c1", initiator: "user_a", callee: me), myUserId: me, now: Date())
        let end = Date()
        store.reduce(.callEnded(CallEndedMessage(callId: "c1", by: "user_a")), myUserId: me, now: end)
        XCTAssertEqual(store.missedCalls.count, 1)
        XCTAssertEqual(store.missedCalls[0].kind, .unanswered)
        XCTAssertEqual(store.missedCalls[0].fromUserId, "user_a")
    }

    func testEndedByMeIsNotMissed() {
        var store = CallStore()
        store.reduce(privateStart(callId: "c1", initiator: "user_a", callee: me), myUserId: me, now: Date())
        store.reduce(.callEnded(CallEndedMessage(callId: "c1", by: me)), myUserId: me, now: Date())
        XCTAssertTrue(store.missedCalls.isEmpty)
    }

    func testCallerDoesNotRecordOwnMissedCall() {
        var store = CallStore()
        store.reduce(privateStart(callId: "c1", initiator: me, callee: "user_b"), myUserId: me, now: Date())
        store.reduce(.callEnded(CallEndedMessage(callId: "c1", by: me)), myUserId: me, now: Date())
        XCTAssertTrue(store.missedCalls.isEmpty)
    }

    func testDeclinedCallIsMissedImmediately() {
        var store = CallStore()
        store.reduce(privateStart(callId: "c1", initiator: "user_a", callee: me), myUserId: me, now: Date())
        store.markDeclined(callId: "c1", myUserId: me, now: Date())
        XCTAssertEqual(store.missedCalls.count, 1)
        XCTAssertEqual(store.missedCalls[0].kind, .declined)
        XCTAssertNil(store.activeCall)
    }

    func testGroupCallEndIsNeverMissed() {
        var store = CallStore()
        store.reduce(groupStart(callId: "c1", groupId: "g1"), myUserId: me, now: Date())
        store.reduce(.callEnded(CallEndedMessage(callId: "c1", by: "user_a")), myUserId: me, now: Date())
        XCTAssertTrue(store.missedCalls.isEmpty)
    }

    func testMissedCallListCaps() {
        var store = CallStore()
        for i in 0..<60 {
            let id = "c\(i)"
            store.reduce(privateStart(callId: id, initiator: "user_a", callee: me), myUserId: me, now: Date())
            store.reduce(.callEnded(CallEndedMessage(callId: id, by: "user_a")), myUserId: me, now: Date())
        }
        XCTAssertEqual(store.missedCalls.count, CallStore.missedCallLimit)
    }

    // MARK: Roster

    func testAffiliationChangedDeduplicates() {
        var roster = RosterStore()
        let change = AffiliationChangedMessage(
            userId: "u1", groupId: "g1",
            state: AffiliationState.affiliated, at: 1_780_000_000_000
        )
        roster.reduce(.affiliationChanged(change), now: Date())
        roster.reduce(.affiliationChanged(change), now: Date())
        XCTAssertEqual(roster.affiliations.count, 1)
    }

    func testAffiliationDeaffiliationRemoves() {
        var roster = RosterStore()
        roster.reduce(
            .affiliationChanged(AffiliationChangedMessage(
                userId: "u1", groupId: "g1",
                state: AffiliationState.affiliated, at: 1_780_000_000_000
            )),
            now: Date()
        )
        roster.reduce(
            .affiliationChanged(AffiliationChangedMessage(
                userId: "u1", groupId: "g1",
                state: AffiliationState.deaffiliated, at: 1_780_000_000_001
            )),
            now: Date()
        )
        XCTAssertTrue(roster.affiliations.isEmpty)
    }

    func testRosterUnknownUserStub() {
        var roster = RosterStore()
        roster.reduce(
            .presenceUpdate(PresenceUpdateMessage(
                userId: "u9", state: PresenceState.online, at: 1_780_000_000_000
            )),
            now: Date()
        )
        XCTAssertEqual(roster.presence["u9"], PresenceState.online)
        XCTAssertEqual(roster.displayName(for: "u9"), "u9",
                       "a stub renders the id until bootstrap fills the name")
    }
}
