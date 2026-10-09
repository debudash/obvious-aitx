// Floor state machine tests — the whole state/event table, mirroring the
// spec's client-facing floor table. These mirror the Android suite's shape.

import XCTest
@testable import MCPTTCore

final class FloorReducerTests: XCTestCase {
    // MARK: Idle

    func testIdlePressSendsRequestAndMovesToRequesting() {
        let t = FloorReducer.reduce(.idle, .pttPressed)
        XCTAssertEqual(t.state, .requesting)
        XCTAssertEqual(t.effects, [.sendFloorRequest])
    }

    func testIdleGrantIsNoOp() {
        let t = FloorReducer.reduce(.idle, .granted(at: 5))
        XCTAssertEqual(t.state, .idle)
        XCTAssertTrue(t.effects.isEmpty)
    }

    // MARK: Requesting

    func testRequestingGrantStartsTransmitWithBurstTimer() {
        let t = FloorReducer.reduce(.requesting, .granted(at: 100))
        XCTAssertEqual(t.state, .granted(burstStartedAt: 100))
        XCTAssertEqual(t.effects, [
            .startTransmitting,
            .startBurstTimer(startedAt: 100, duration: 60),
            .dismissDeniedToast,
            .dismissPreemptBanner,
        ])
    }

    func testRequestingDeniedShowsToast() {
        let t = FloorReducer.reduce(.requesting, .denied(reason: "busy", queuePosition: 2))
        XCTAssertEqual(t.state, .denied(reason: "busy"))
        XCTAssertEqual(t.effects, [.showDeniedToast(reason: "busy"), .dismissPreemptBanner])
    }

    func testRequestingReleaseCancelsInFlightRequest() {
        let t = FloorReducer.reduce(.requesting, .pttReleased)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.sendFloorRelease])
    }

    // MARK: Granted

    func testGrantedReleaseStopsTransmitAndTellsServer() {
        let t = FloorReducer.reduce(.granted(burstStartedAt: 10), .pttReleased)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.stopTransmitting, .sendFloorRelease, .cancelBurstTimer])
    }

    func testGrantedBurstExpiryMirrorsServerExpiry() {
        let t = FloorReducer.reduce(.granted(burstStartedAt: 10), .burstExpired)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.stopTransmitting, .sendFloorRelease, .cancelBurstTimer])
    }

    func testGrantedPreemptWhileHoldingPttRejoinsQueue() {
        let t = FloorReducer.reduce(
            .granted(burstStartedAt: 10),
            .preempted(by: "net_control", emergency: true, pttHeld: true)
        )
        XCTAssertEqual(t.state, .requesting)
        XCTAssertEqual(t.effects, [
            .stopTransmitting,
            .cancelBurstTimer,
            .showPreemptBanner(by: "net_control", emergency: true),
            .sendFloorRequest,
        ])
    }

    func testGrantedPreemptWhileReleasedGoesToPreempted() {
        let t = FloorReducer.reduce(
            .granted(burstStartedAt: 10),
            .preempted(by: "net_control", emergency: false, pttHeld: false)
        )
        XCTAssertEqual(t.state, .preempted(by: "net_control", emergency: false))
        XCTAssertEqual(t.effects, [
            .stopTransmitting,
            .cancelBurstTimer,
            .showPreemptBanner(by: "net_control", emergency: false),
        ])
    }

    func testGrantedDenialIsDispatchRevoke() {
        let t = FloorReducer.reduce(.granted(burstStartedAt: 10), .denied(reason: "net-control", queuePosition: 0))
        XCTAssertEqual(t.state, .denied(reason: "net-control"))
        XCTAssertEqual(t.effects, [
            .stopTransmitting,
            .cancelBurstTimer,
            .showDeniedToast(reason: "net-control"),
        ])
    }

    // MARK: Queued

    func testQueuedGrantPromotesToGranted() {
        let t = FloorReducer.reduce(.queued(position: 1), .granted(at: 42))
        XCTAssertEqual(t.state, .granted(burstStartedAt: 42))
        XCTAssertEqual(t.effects.first, .startTransmitting)
    }

    func testQueuedReleaseCancelsEntry() {
        let t = FloorReducer.reduce(.queued(position: 2), .pttReleased)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.sendFloorRelease])
    }

    // MARK: Denied

    func testDeniedFreshPressAcknowledgesAndReRequests() {
        let t = FloorReducer.reduce(.denied(reason: "busy"), .pttPressed)
        XCTAssertEqual(t.state, .requesting)
        XCTAssertEqual(t.effects, [.sendFloorRequest, .dismissDeniedToast])
    }

    func testDeniedGrantBeatsStaleDenial() {
        let t = FloorReducer.reduce(.denied(reason: "busy"), .granted(at: 7))
        XCTAssertEqual(t.state, .granted(burstStartedAt: 7))
    }

    func testDeniedAcknowledgeReturnsToIdle() {
        let t = FloorReducer.reduce(.denied(reason: "x"), .denyAcknowledged)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.dismissDeniedToast])
    }

    // MARK: Preempted

    func testPreemptedPressReRequests() {
        let t = FloorReducer.reduce(.preempted(by: "a", emergency: true), .pttPressed)
        XCTAssertEqual(t.state, .requesting)
        XCTAssertEqual(t.effects, [.sendFloorRequest, .dismissPreemptBanner])
    }

    func testPreemptedReleaseReturnsToIdle() {
        let t = FloorReducer.reduce(.preempted(by: "a", emergency: true), .pttReleased)
        XCTAssertEqual(t.state, .idle)
        XCTAssertEqual(t.effects, [.dismissPreemptBanner])
    }

    func testSecondPreemptUpdatesBanner() {
        let t = FloorReducer.reduce(
            .preempted(by: "a", emergency: true),
            .preempted(by: "b", emergency: false, pttHeld: false)
        )
        XCTAssertEqual(t.state, .preempted(by: "b", emergency: false))
        XCTAssertEqual(t.effects, [.showPreemptBanner(by: "b", emergency: false)])
    }

    // MARK: Call end

    func testCallEndedResetsEveryStateToIdleWithCleanup() {
        let states: [FloorState] = [
            .idle,
            .requesting,
            .granted(burstStartedAt: 1),
            .queued(position: 1),
            .denied(reason: "x"),
            .preempted(by: "a", emergency: false),
        ]
        for state in states {
            let t = FloorReducer.reduce(state, .callEnded)
            XCTAssertEqual(t.state, .idle, "callEnded from \(state) must reach idle")
        }
        // Live cleanup effects only where something was live.
        XCTAssertEqual(
            FloorReducer.reduce(.granted(burstStartedAt: 1), .callEnded).effects,
            [.stopTransmitting, .cancelBurstTimer, .dismissDeniedToast, .dismissPreemptBanner]
        )
        XCTAssertEqual(FloorReducer.reduce(.idle, .callEnded).effects, [])
        XCTAssertEqual(FloorReducer.reduce(.denied(reason: "x"), .callEnded).effects, [.dismissDeniedToast])
    }

    // MARK: Timer math

    func testDefaultMaxTalkDurationMatchesServer() {
        XCTAssertEqual(FloorReducer.defaultMaxTalkDuration, 60)
    }

    func testCustomMaxTalkDurationRidesTheTimerEffect() {
        let t = FloorReducer.reduce(.requesting, .granted(at: 0), maxTalkDuration: 30)
        guard case let .startBurstTimer(_, duration) = t.effects[1] else {
            return XCTFail("expected burst timer effect")
        }
        XCTAssertEqual(duration, 30)
    }
}
