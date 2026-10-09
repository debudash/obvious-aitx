// REST wire-model tests: the Go RFC3339 variants the server emits, DTO
// decode from real server JSON shapes, and round-tripping.

import XCTest
@testable import MCPTTCore

final class WireModelsTests: XCTestCase {
    // MARK: GoTime

    func testGoTimePlainZ() throws {
        let d = try GoTime.decode("2026-10-09T20:42:25Z")
        let cal = Calendar(identifier: .gregorian)
        var c = cal.dateComponents(in: TimeZone(identifier: "UTC")!, from: d)
        c.timeZone = TimeZone(identifier: "UTC")
        XCTAssertEqual(c.year, 2026)
        XCTAssertEqual(c.month, 10)
        XCTAssertEqual(c.day, 9)
        XCTAssertEqual(c.hour, 20)
        XCTAssertEqual(c.minute, 42)
        XCTAssertEqual(c.second, 25)
    }

    func testGoTimeFractionalSecondsUpToNineDigits() throws {
        let a = try GoTime.decode("2026-10-09T20:42:25.1Z")
        let b = try GoTime.decode("2026-10-09T20:42:25.123456789Z")
        let plain = try GoTime.decode("2026-10-09T20:42:25Z")
        XCTAssertEqual(a.timeIntervalSince(plain), 0.1, accuracy: 0.0001)
        XCTAssertEqual(b.timeIntervalSince(plain), 0.123456789, accuracy: 1e-6)
    }

    func testGoTimeOffsets() throws {
        let z = try GoTime.decode("2026-10-09T20:42:25Z")
        let plus = try GoTime.decode("2026-10-09T23:42:25+03:00")
        let minus = try GoTime.decode("2026-10-09T14:42:25-06:00")
        XCTAssertEqual(z.timeIntervalSince1970, plus.timeIntervalSince1970, accuracy: 0.001)
        XCTAssertEqual(z.timeIntervalSince1970, minus.timeIntervalSince1970, accuracy: 0.001)
    }

    func testGoTimeLowercaseZ() throws {
        let a = try GoTime.decode("2026-10-09T20:42:25z")
        let b = try GoTime.decode("2026-10-09T20:42:25Z")
        XCTAssertEqual(a, b)
    }

    func testGoTimeRejectsGarbage() {
        for bad in ["", "not-a-time", "2026-10-09 20:42:25Z", "2026-13-09T20:42:25X"] {
            XCTAssertThrowsError(try GoTime.decode(bad)) { error in
                XCTAssertTrue(error is DecodingError)
            }
        }
    }

    func testGoTimeEncodeRoundTrip() throws {
        let original = Date(timeIntervalSince1970: 1_780_000_000.123)
        let back = try GoTime.decode(GoTime.encode(original))
        XCTAssertEqual(back.timeIntervalSince1970, original.timeIntervalSince1970, accuracy: 0.001)
    }

    // MARK: Server DTO shapes

    func testUserDTODecodesFromServerShape() throws {
        // api.userDTO as the login handler writes it.
        let raw = """
        {"id":"usr_1","username":"bravo","displayName":"Bravo","role":"field","priority":5,"functionalAlias":null,"createdAt":"2026-10-09T20:42:25.123456789Z"}
        """
        let user = try JSONDecoder().decode(UserDTO.self, from: Data(raw.utf8))
        XCTAssertEqual(user.id, "usr_1")
        XCTAssertEqual(user.priority, 5)
        XCTAssertNil(user.functionalAlias)
        XCTAssertFalse(user.isDispatcher)
    }

    func testUserDTOSurvivesUnknownRole() throws {
        let raw = """
        {"id":"u","username":"x","displayName":"X","role":"administrator-extraordinaire","priority":3,"functionalAlias":"S1","createdAt":"2026-10-09T20:42:25Z"}
        """
        let user = try JSONDecoder().decode(UserDTO.self, from: Data(raw.utf8))
        XCTAssertEqual(user.functionalAlias, "S1")
    }

    func testCallSnapshotDTODecodes() throws {
        let raw = """
        {"callId":"c1","kind":"group","groupId":"g1","floorControl":true,"emergency":false,"imminentPeril":false,"participants":["a","b"],"speaker":"a","speakerSince":"2026-10-09T20:42:25Z","queue":["b"]}
        """
        let call = try JSONDecoder().decode(CallSnapshotDTO.self, from: Data(raw.utf8))
        XCTAssertEqual(call.kind, .group)
        XCTAssertEqual(call.queue, ["b"])
        XCTAssertEqual(call.participants.count, 2)
    }

    func testCallSnapshotWithoutSpeakerSince() throws {
        let raw = """
        {"callId":"c1","kind":"private","groupId":null,"floorControl":true,"emergency":true,"imminentPeril":false,"participants":["a"],"speaker":null,"speakerSince":null,"queue":null}
        """
        let call = try JSONDecoder().decode(CallSnapshotDTO.self, from: Data(raw.utf8))
        XCTAssertEqual(call.kind, .privateCall)
        XCTAssertTrue(call.emergency)
        XCTAssertNil(call.speakerSince)
    }

    func testFloorDecisionDTODecodes() throws {
        let raw = """
        {"userId":"u1","outcome":"queued","priority":7,"emergency":false,"token":null,"queuePosition":1,"reason":null,"preemptedUserId":null}
        """
        let decision = try JSONDecoder().decode(FloorDecisionDTO.self, from: Data(raw.utf8))
        XCTAssertEqual(decision.outcome, "queued")
        XCTAssertFalse(decision.isGranted)
    }

    func testLoginResponseDecodes() throws {
        let raw = """
        {"user":{"id":"u1","username":"bravo","displayName":"Bravo","role":"dispatcher","priority":10,"functionalAlias":"Dispatch","createdAt":"2026-10-09T20:42:25Z"},"token":"jwt-here"}
        """
        let response = try JSONDecoder().decode(LoginResponse.self, from: Data(raw.utf8))
        XCTAssertEqual(response.user.role, "dispatcher")
        XCTAssertTrue(response.user.isDispatcher)
        XCTAssertEqual(response.token, "jwt-here")
    }

    func testEmergencyCallResponseDecodes() throws {
        let raw = """
        {"call":{"callId":"c9","kind":"group","groupId":"g1","floorControl":true,"emergency":true,"imminentPeril":false,"participants":["u1"],"speaker":"u1","speakerSince":"2026-10-09T20:42:25Z","queue":[]},"decision":{"userId":"u1","outcome":"granted","priority":9,"emergency":true,"token":null,"queuePosition":null,"reason":null,"preemptedUserId":null},"alert":{"id":"al1","userId":"u1","callId":"c9","kind":"emergency","lat":47.6,"lon":-122.3,"note":null,"status":"active","acknowledgedBy":null,"createdAt":"2026-10-09T20:42:25Z"}}
        """
        let response = try JSONDecoder().decode(EmergencyCallResponse.self, from: Data(raw.utf8))
        XCTAssertTrue(response.call.emergency)
        XCTAssertTrue(response.decision.isGranted)
        XCTAssertEqual(response.alert.callId, "c9")
    }

    func testEmergencyRequestEncodesOmitsNils() throws {
        let body = EmergencyCallRequest(groupId: "g1", imminentPeril: false, lat: 1.0, lon: 2.0)
        let data = try JSONEncoder().encode(body)
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"groupId\":\"g1\""))
        XCTAssertFalse(json.contains("callId"), "nil callId must not be sent")
    }

    func testAlertDTODecodesOffsetTimeForm() throws {
        // alertDTO stamps "2006-01-02T15:04:05Z07:00" — offset form.
        let raw = """
        {"id":"a1","userId":"u1","callId":null,"kind":"imminent-peril","lat":null,"lon":null,"note":"fuel leak","status":"active","acknowledgedBy":null,"createdAt":"2026-10-09T20:42:25-07:00"}
        """
        let alert = try JSONDecoder().decode(AlertDTO.self, from: Data(raw.utf8))
        XCTAssertTrue(alert.isImminentPeril)
        XCTAssertTrue(alert.isActive)
        XCTAssertEqual(alert.note, "fuel leak")
    }
}
