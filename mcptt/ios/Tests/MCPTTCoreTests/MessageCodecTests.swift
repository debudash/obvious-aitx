// Protocol-layer tests: frame decode/encode parity with the server's
// signaling (server/internal/protocol/protocol.go) — every pinned wire type
// round-trips, unknown types decode to .unknown instead of failing.

import XCTest
@testable import MCPTTCore

final class MessageCodecTests: XCTestCase {
    private func decode(_ raw: String) -> Result<InboundMessage, MessageCodecError> {
        MessageCodec.decode(Data(raw.utf8))
    }

    // MARK: Inbound decode

    func testDecodesFloorGranted() throws {
        let raw = """
        {"type":"FloorGranted","callId":"call_9f3","userId":"radio_bravo","queue":["radio_charlie"]}
        """
        guard case let .success(.floorGranted(grant)) = decode(raw) else {
            return XCTFail("expected floorGranted")
        }
        XCTAssertEqual(grant.callId, "call_9f3")
        XCTAssertEqual(grant.userId, "radio_bravo")
        XCTAssertEqual(grant.queue, ["radio_charlie"])
    }

    func testFloorGrantedQueueNullDecodesToEmpty() throws {
        // The server fan-outs queue:null when no one waits (revoke with no
        // successor).
        let raw = """
        {"type":"FloorGranted","callId":"c1","userId":"u1","queue":null}
        """
        guard case let .success(.floorGranted(grant)) = decode(raw) else {
            return XCTFail("expected floorGranted")
        }
        XCTAssertEqual(grant.queue ?? [], [])
    }

    func testDecodesFloorDeniedWithReasonAndPosition() throws {
        let raw = """
        {"type":"FloorDenied","callId":"c1","userId":"u2","reason":"busy","queuePosition":1}
        """
        guard case let .success(.floorDenied(denial)) = decode(raw) else {
            return XCTFail("expected floorDenied")
        }
        XCTAssertEqual(denial.reason, "busy")
        XCTAssertEqual(denial.queuePosition, 1)
    }

    func testDecodesFloorDeniedWithoutOptionalFields() throws {
        // reason/queuePosition are omitempty server-side.
        let raw = """
        {"type":"FloorDenied","callId":"c1","userId":"u2"}
        """
        guard case let .success(.floorDenied(denial)) = decode(raw) else {
            return XCTFail("expected floorDenied")
        }
        XCTAssertEqual(denial.reason, "")
        XCTAssertEqual(denial.queuePosition, 0)
    }

    func testDecodesFloorPreempted() throws {
        let raw = """
        {"type":"FloorPreempted","callId":"c1","by":"radio_alpha","emergency":true}
        """
        guard case let .success(.floorPreempted(preempt)) = decode(raw) else {
            return XCTFail("expected floorPreempted")
        }
        XCTAssertEqual(preempt.by, "radio_alpha")
        XCTAssertEqual(preempt.emergency, true)
    }

    func testDecodesFloorReleased() throws {
        let raw = """
        {"type":"FloorReleased","callId":"c1","userId":"u1"}
        """
        guard case let .success(.floorReleased(release)) = decode(raw) else {
            return XCTFail("expected floorReleased")
        }
        XCTAssertEqual(release.callId, "c1")
        XCTAssertEqual(release.userId, "u1")
    }

    func testDecodesCallStartedGroup() throws {
        let raw = """
        {"type":"CallStart","callId":"c1","groupId":"g1","kind":"group","initiatorId":"u1"}
        """
        guard case let .success(.callStarted(start)) = decode(raw) else {
            return XCTFail("expected callStarted")
        }
        XCTAssertEqual(start.kind, .group)
        XCTAssertEqual(start.groupId, "g1")
        XCTAssertEqual(start.initiatorId, "u1")
        XCTAssertNil(start.calleeId, "callee is not in the pinned contract yet")
    }

    func testDecodesCallStartedPrivateWithCalleeWhenPresent() throws {
        // Optimistic callee decoding — appears the day the server sends it.
        let raw = """
        {"type":"CallStart","callId":"c1","kind":"private","initiatorId":"u1","calleeId":"u2"}
        """
        guard case let .success(.callStarted(start)) = decode(raw) else {
            return XCTFail("expected callStarted")
        }
        XCTAssertEqual(start.kind, .privateCall)
        XCTAssertEqual(start.calleeId, "u2")
    }

    func testDecodesCallJoined() throws {
        let raw = """
        {"type":"CallJoined","callId":"c1","userId":"u2"}
        """
        guard case let .success(.callJoined(join)) = decode(raw) else {
            return XCTFail("expected callJoined")
        }
        XCTAssertEqual(join.userId, "u2")
    }

    func testDecodesCallEnded() throws {
        let raw = """
        {"type":"CallEnded","callId":"c1","by":"dispatcher_1"}
        """
        guard case let .success(.callEnded(ended)) = decode(raw) else {
            return XCTFail("expected callEnded")
        }
        XCTAssertEqual(ended.by, "dispatcher_1")
    }

    func testDecodesParticipantRemoved() throws {
        let raw = """
        {"type":"ParticipantRemoved","callId":"c1","userId":"u2","by":"dispatcher_1"}
        """
        guard case let .success(.participantRemoved(removed)) = decode(raw) else {
            return XCTFail("expected participantRemoved")
        }
        XCTAssertEqual(removed.userId, "u2")
        XCTAssertEqual(removed.by, "dispatcher_1")
    }

    func testDecodesEmergencyAlertWithLocation() throws {
        let raw = """
        {"type":"EmergencyAlert","alertId":"a1","userId":"u1","callId":"c9","lat":47.6,"lon":-122.3,"emergency":true,"note":"fuel leak"}
        """
        guard case let .success(.emergencyAlert(alert)) = decode(raw) else {
            return XCTFail("expected emergencyAlert")
        }
        XCTAssertEqual(alert.alertId, "a1")
        XCTAssertEqual(alert.callId, "c9")
        XCTAssertEqual(alert.lat ?? 0, 47.6, accuracy: 0.0001)
        XCTAssertEqual(alert.note, "fuel leak")
    }

    func testDecodesEmergencyAlertAck() throws {
        let raw = """
        {"type":"EmergencyAlertAck","alertId":"a1","acknowledgedBy":"dispatcher_1"}
        """
        guard case let .success(.emergencyAlertAck(ack)) = decode(raw) else {
            return XCTFail("expected emergencyAlertAck")
        }
        XCTAssertEqual(ack.acknowledgedBy, "dispatcher_1")
    }

    func testDecodesPresenceUpdate() throws {
        let raw = """
        {"type":"PresenceUpdate","userId":"u1","state":"online","at":1780000000000}
        """
        guard case let .success(.presenceUpdate(presence)) = decode(raw) else {
            return XCTFail("expected presenceUpdate")
        }
        XCTAssertEqual(presence.state, "online")
        XCTAssertEqual(presence.at, 1_780_000_000_000)
    }

    func testDecodesAffiliationChanged() throws {
        let raw = """
        {"type":"AffiliationChanged","userId":"u1","groupId":"g1","state":"affiliated","at":1780000000000}
        """
        guard case let .success(.affiliationChanged(changed)) = decode(raw) else {
            return XCTFail("expected affiliationChanged")
        }
        XCTAssertEqual(changed.state, "affiliated")
    }

    func testDecodesMediaAnswerWithError() throws {
        let raw = """
        {"type":"MediaAnswer","callId":"c1","sdp":null,"err":"bad token"}
        """
        guard case let .success(.mediaAnswer(answer)) = decode(raw) else {
            return XCTFail("expected mediaAnswer")
        }
        XCTAssertNil(answer.sdp)
        XCTAssertEqual(answer.err, "bad token")
    }

    func testUnknownTypeDecodesToUnknownNotFailure() throws {
        let raw = """
        {"type":"FutureServerEvent","callId":"c1"}
        """
        guard case let .success(.unknown(type)) = decode(raw) else {
            return XCTFail("expected .unknown")
        }
        XCTAssertEqual(type, "FutureServerEvent")
    }

    func testMalformedEnvelopeFailsVisibly() {
        guard case let .failure(error) = decode("not json at all") else {
            return XCTFail("expected failure")
        }
        XCTAssertEqual(error, .malformedEnvelope)
    }

    func testMalformedPayloadOfKnownTypeFailsVisibly() {
        let raw = """
        {"type":"FloorGranted","callId":42}
        """
        guard case let .failure(error) = decode(raw) else {
            return XCTFail("expected failure")
        }
        XCTAssertEqual(error, .malformedPayload(type: "FloorGranted"))
    }

    // MARK: Outbound encode

    func testEncodesFloorRequestWithPriority() throws {
        let message = FloorRequestMessage(
            callId: "c1", userId: "u1", priority: 7, emergency: false
        )
        let data = try MessageCodec.encode(message)
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"type\":\"FloorRequest\""))
        XCTAssertTrue(json.contains("\"priority\":7"))
        XCTAssertTrue(json.contains("\"userId\":\"u1\""))
    }

    func testEncodesFloorReleased() throws {
        let data = try MessageCodec.encode(
            FloorReleasedMessage(callId: "c1", userId: "u1")
        )
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"type\":\"FloorReleased\""))
    }

    func testEncodesCallStartForGroup() throws {
        let data = try MessageCodec.encode(CallStartMessage(
            callId: "local-1", groupId: "g1", kind: .group, initiatorId: "u1"
        ))
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"type\":\"CallStart\""))
        XCTAssertTrue(json.contains("\"kind\":\"group\""))
    }

    func testEncodesCallStartForPrivateWithCallee() throws {
        let data = try MessageCodec.encode(CallStartMessage(
            callId: "local-2", groupId: nil, kind: .privateCall,
            initiatorId: "u1", calleeId: "u2"
        ))
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"kind\":\"private\""))
        XCTAssertTrue(json.contains("\"calleeId\":\"u2\""))
    }

    func testEncodesMediaOfferWithToken() throws {
        let data = try MessageCodec.encode(
            MediaOfferMessage(callId: "c1", token: "tok", sdp: "v=0")
        )
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"type\":\"MediaOffer\""))
        XCTAssertTrue(json.contains("\"token\":\"tok\""))
    }

    func testEncodesEmergencyAlertFrame() throws {
        let data = try MessageCodec.encode(EmergencyAlertMessage(
            alertId: "local-a", userId: "u1", callId: nil,
            emergency: true, imminentPeril: false
        ))
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"type\":\"EmergencyAlert\""))
        XCTAssertTrue(json.contains("\"alertId\":\"local-a\""))
        XCTAssertFalse(json.contains("\"callId\""), "nil callId must be omitted")
    }
}
