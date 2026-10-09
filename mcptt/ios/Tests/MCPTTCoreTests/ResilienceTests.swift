// Reconnect/backoff and alert-retry coverage — the Android suite's
// resilience shape mirrored on iOS.

import XCTest
@testable import MCPTTCore

final class ResilienceTests: XCTestCase {
    // MARK: Backoff schedule

    func testBackoffDoublesAndCaps() {
        let schedule = BackoffSchedule(base: 1, factor: 2, maxDelay: 30)
        XCTAssertEqual(schedule.delay(forAttempt: 1), 1)
        XCTAssertEqual(schedule.delay(forAttempt: 2), 2)
        XCTAssertEqual(schedule.delay(forAttempt: 3), 4)
        XCTAssertEqual(schedule.delay(forAttempt: 6), 30, "capped at maxDelay")
        XCTAssertEqual(schedule.delay(forAttempt: 20), 30)
    }

    func testBackoffFullJitterStaysWithinDelay() {
        let schedule = BackoffSchedule(base: 2, factor: 2, maxDelay: 30)
        // Attempt 4: 2·2^3 = 16, below the cap — full jitter halves it.
        let jittered = schedule.jitteredDelay(forAttempt: 4, random: 0.5)
        XCTAssertEqual(jittered, 8, accuracy: 0.0001)
        let zero = schedule.jitteredDelay(forAttempt: 4, random: 0)
        XCTAssertEqual(zero, 0, accuracy: 0.0001)
    }

    func testBackoffClampsMalformedRandom() {
        let schedule = BackoffSchedule(base: 2, factor: 2, maxDelay: 30)
        // Above 1 clamps to <1 — attempt 5 is capped at 30 anyway.
        let capped = schedule.jitteredDelay(forAttempt: 5, random: 5)
        XCTAssertEqual(capped, 30, accuracy: 0.001)
        XCTAssertEqual(schedule.jitteredDelay(forAttempt: 1, random: -3), 0, accuracy: 0.001)
    }

    func testReconnectStateResetsOnConnection() {
        var state = ReconnectState()
        state.recordDrop()
        state.recordDrop()
        state.recordDrop()
        XCTAssertEqual(state.nextAttempt, 4)
        state.recordConnection()
        XCTAssertEqual(state.nextAttempt, 1)
        state.recordConnection()
        XCTAssertEqual(state.nextAttempt, 1, "double connect must not accumulate attempts")
    }

    // MARK: Alert retry queue

    func testAlertRetryDueImmediatelyWhenNeverAttempted() {
        var queue = AlertRetryQueue()
        queue.enqueue(PendingAlert(id: "a1", groupId: "g1", imminentPeril: false))
        XCTAssertTrue(queue.due(now: Date()).map { $0.id }.contains("a1"))
    }

    func testAlertRetryBackoffSchedule() {
        XCTAssertEqual(AlertRetryQueue.delay(forAttempt: 1), 2)
        XCTAssertEqual(AlertRetryQueue.delay(forAttempt: 2), 4)
        XCTAssertEqual(AlertRetryQueue.delay(forAttempt: 3), 8)
        XCTAssertEqual(AlertRetryQueue.delay(forAttempt: 10), 15, "capped at 15 s")
    }

    func testAlertRetryNotDueBeforeBackoffElapses() throws {
        var queue = AlertRetryQueue()
        let alert = PendingAlert(id: "a1", groupId: nil, imminentPeril: false)
        queue.enqueue(alert)
        let t0 = Date(timeIntervalSince1970: 1_000)
        queue.recordAttempt(id: "a1", at: t0)
        let notDue = queue.alert(id: "a1").map { queue.isDue($0, now: t0.addingTimeInterval(1)) }
        XCTAssertEqual(notDue, false)
        let due = queue.alert(id: "a1").map { queue.isDue($0, now: t0.addingTimeInterval(2)) }
        XCTAssertEqual(due, true)
    }

    func testAlertRetryAttemptIncrementKeepsAlertQueued() {
        var queue = AlertRetryQueue()
        queue.enqueue(PendingAlert(id: "a1", groupId: nil, imminentPeril: false))
        queue.recordAttempt(id: "a1", at: Date())
        XCTAssertEqual(queue.alert(id: "a1")?.attempts, 1)
        XCTAssertFalse(queue.isEmpty, "a failed attempt must not remove the alert")
    }

    func testAlertAcknowledgeRemoves() {
        var queue = AlertRetryQueue()
        queue.enqueue(PendingAlert(id: "a1", groupId: nil, imminentPeril: false))
        queue.acknowledge(id: "a1")
        XCTAssertTrue(queue.isEmpty)
    }

    func testAlertDuplicateEnqueueIgnored() {
        var queue = AlertRetryQueue()
        queue.enqueue(PendingAlert(id: "a1", groupId: nil, imminentPeril: false))
        queue.enqueue(PendingAlert(id: "a1", groupId: nil, imminentPeril: true))
        XCTAssertEqual(queue.pending.count, 1)
        XCTAssertEqual(queue.pending[0].imminentPeril, false, "first entry wins")
    }

    func testAlertRetryUntilAcknowledgedThroughManyDrops() {
        // Simulate five failed submits: attempts advance, alert persists.
        var queue = AlertRetryQueue()
        queue.enqueue(PendingAlert(id: "a1", groupId: "g", imminentPeril: false))
        var now = Date()
        for _ in 1...5 {
            queue.recordAttempt(id: "a1", at: now)
            now = now.addingTimeInterval(16) // past the 15 s cap
        }
        XCTAssertEqual(queue.alert(id: "a1")?.attempts, 5)
        XCTAssertTrue(queue.due(now: now).contains { $0.id == "a1" })
        queue.acknowledge(id: "a1")
        XCTAssertTrue(queue.isEmpty)
    }
}
