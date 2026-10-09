// Emergency orchestration: the guarded hold gesture (1.5 s + confirmation)
// fans out to an alert (always) and an optional emergency call. The
// voiceless alert is durable: it sits in the retry queue and re-submits on
// capped backoff until the server has it (HTTP 2xx) or a dispatcher
// acknowledges the fan-out. The emergency CALL is fire-and-report —
// retrying it wholesale would re-trigger pre-empting calls.

import Foundation
import MCPTTCore

@MainActor
final class EmergencyController: ObservableObject {
    /// The armed hold — set when the 1.5 s press completes, cleared when
    /// the confirmation sheet resolves.
    @Published var confirmationPending = false

    private var queue = AlertRetryQueue()
    private let api: APIClient
    private let token: String
    private let location: LocationProvider
    private var retryLoop: Task<Void, Never>?

    init(api: APIClient, token: String, location: LocationProvider = LocationProvider()) {
        self.api = api
        self.token = token
        self.location = location
    }

    // MARK: The guarded gesture

    /// Press-and-hold reached 1.5 s — arm the confirmation sheet.
    func holdCompleted() {
        confirmationPending = true
    }

    /// The sheet's Cancel — no alert goes out.
    func cancelHold() {
        confirmationPending = false
    }

    /// The sheet's Confirm. `withCall` fires POST /api/calls/emergency
    /// (escalating `callId` when the user is in a call, else starting an
    /// emergency group call on `groupId`) — the server pre-empts the floor
    /// and attaches the alert. The voiceless alert goes into the retry
    /// queue, whose runner re-submits until the server acknowledges.
    func confirm(groupId: String?, callId: String?, withCall: Bool, imminentPeril: Bool) async {
        confirmationPending = false
        let fix = await location.currentLocation()

        if withCall {
            // Exactly one of callId / groupId (server rule).
            let body = EmergencyCallRequest(
                callId: callId, groupId: callId == nil ? groupId : nil,
                imminentPeril: imminentPeril,
                lat: fix?.0, lon: fix?.1
            )
            do {
                _ = try await api.emergencyCall(body, token: token)
            } catch {
                NSLog("mcptt: emergency call failed: \(error)")
            }
            return
        }

        let alert = PendingAlert(
            id: UUID().uuidString,
            groupId: nil, // voiceless alerts take no call or group
            imminentPeril: imminentPeril,
            lat: fix?.0,
            lon: fix?.1
        )
        queue.enqueue(alert)
        startRetryLoop()
    }

    /// The server acknowledged an alert (HTTP 2xx on submit, or the
    /// EmergencyAlertAck fan-out confirmed it) — stop retrying it.
    func acknowledge(id: String) {
        queue.acknowledge(id: id)
    }

    /// Fan-out alias used by the session controller.
    func serverAck(alertId: String) {
        acknowledge(id: alertId)
    }

    var pendingCount: Int { queue.pending.count }

    // MARK: Retry loop

    private func startRetryLoop() {
        guard retryLoop == nil else { return }
        retryLoop = Task { [weak self] in
            await self?.runRetryLoop()
        }
    }

    /// Submits due alerts until the queue drains, then parks itself.
    private func runRetryLoop() async {
        while !queue.isEmpty {
            let due = queue.due(now: Date())
            for alert in due {
                queue.recordAttempt(id: alert.id, at: Date())
                do {
                    // Voiceless path: no callId/groupId on the wire.
                    _ = try await api.raiseAlert(
                        EmergencyCallRequest(
                            imminentPeril: alert.imminentPeril,
                            lat: alert.lat,
                            lon: alert.lon,
                            note: alert.note
                        ),
                        token: token
                    )
                    // HTTP 2xx: the server holds the alert — acknowledged.
                    queue.acknowledge(id: alert.id)
                } catch {
                    // Stay queued; the backoff schedule sets the next due.
                    continue
                }
            }
            if queue.isEmpty { break }
            try? await Task.sleep(nanoseconds: UInt64(500_000_000))
        }
        retryLoop = nil
    }
}
