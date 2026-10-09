// Audio-session policy and media-plan tests — "audio-session configuration
// unit-tested where possible" (no AVFAudio in this environment; the plan's
// string values carry the policy).

import XCTest
@testable import MCPTTCore

final class AudioMediaPolicyTests: XCTestCase {
    func testBasePlanIsPlayAndRecordVoiceChat() {
        let config = AudioSessionPlan().configuration
        XCTAssertEqual(config.category, "playAndRecord")
        XCTAssertEqual(config.mode, "voiceChat")
        XCTAssertEqual(config.routeOverride, "none")
    }

    func testSpeakerRouteOverrideWhileTransmitting() {
        var plan = AudioSessionPlan()
        plan.route = .speaker
        XCTAssertEqual(plan.configuration.routeOverride, "speaker")
        plan.route = .earpiece
        XCTAssertEqual(plan.configuration.routeOverride, "none")
    }

    func testMediaDirectionRecvOnlyUntilGrant() {
        let plan = MediaPlan(roomToken: "t", callId: "c1")
        XCTAssertEqual(plan.direction(for: .idle), .recvOnly)
        XCTAssertEqual(plan.direction(for: .requesting), .recvOnly)
        XCTAssertEqual(plan.direction(for: .queued(position: 1)), .recvOnly)
        XCTAssertEqual(plan.direction(for: .denied(reason: "x")), .recvOnly)
        XCTAssertEqual(plan.direction(for: .preempted(by: "a", emergency: false)), .recvOnly)
        XCTAssertEqual(plan.direction(for: .granted(burstStartedAt: 0)), .sendRecv)
    }

    func testMicLiveExactlyWhenGranted() {
        let plan = MediaPlan(roomToken: "t", callId: "c1")
        for state in [FloorState.idle, .requesting, .queued(position: 1),
                      .denied(reason: "x"), .preempted(by: "a", emergency: false)] {
            XCTAssertFalse(plan.micLive(for: state), "mic must be dead in \(state)")
        }
        XCTAssertTrue(plan.micLive(for: .granted(burstStartedAt: 0)))
    }

    func testAudioOnlyConstraints() {
        XCTAssertEqual(MediaPlan.audioOnlyConstraints["OfferToReceiveAudio"], "true")
        XCTAssertEqual(MediaPlan.audioOnlyConstraints["OfferToReceiveVideo"], "false")
        XCTAssertEqual(MediaPlan.audioOnlyConstraints["DtlsSrtpKeyAgreement"], "true")
    }
}
