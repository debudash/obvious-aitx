// The session orchestrator: owns the signaling client and reconnect loop,
// pumps wire frames through the pure stores (FloorReducer, CallStore,
// RosterStore), executes reducer effects (send frames, gate the mic, run
// the burst timer), and publishes the combined state for the screens.
//
// Threading: all signaling callbacks run on the signaling queue and hop to
// the main actor before touching stores; the burst timer is a Task.

import Foundation
import Combine
import MCPTTCore

struct SessionConfig {
    let baseURL: URL
    let token: String
    let userId: String
    let priority: Int
}

@MainActor
final class SessionController: ObservableObject {
    // MARK: Published state (what the screens render)

    @Published private(set) var roster = RosterStore()
    @Published private(set) var calls = CallStore()
    @Published private(set) var floor: FloorState = .idle
    @Published private(set) var connection: ConnectionState = .idle
    @Published private(set) var burstRemaining: TimeInterval = 0
    @Published private(set) var deniedToast: String?
    @Published private(set) var preemptBanner: String?
    @Published private(set) var alerts: [AlertDTO] = []
    @Published private(set) var ringingCall: ActiveCall?

    /// Set by AppModel to observe connection transitions.
    var onConnectionChange: ((ConnectionState) -> Void)?

    private let api: APIClient
    private let config: SessionConfig
    private let signalingQueue = DispatchQueue(label: "mcptt.signaling")
    private var signaling: SignalingClient?
    private var media: MediaClient?
    private var backoff = BackoffSchedule()
    private var reconnect = ReconnectState()
    private var reconnectTask: Task<Void, Never>?
    private var burstTimer: Task<Void, Never>?
    private var pttHeld = false
    private var stopped = false

    init(api: APIClient, config: SessionConfig, initial: RosterBootstrap) {
        self.api = api
        self.config = config
        roster.set(users: initial.users)
        roster.set(groups: initial.groups)
        roster.set(affiliations: initial.affiliations)
        media = MediaClient(config: config)
    }

    // MARK: Lifecycle

    func start() {
        stopped = false
        // The media leg's offers ride the same signaling socket.
        media?.onOffer = { [weak self] offer in
            self?.signaling?.send(.mediaOffer(offer))
        }
        openSocket()
        Task { await refreshAlerts() }
    }

    func stop() {
        stopped = true
        reconnectTask?.cancel()
        burstTimer?.cancel()
        media?.close()
        signaling?.disconnect()
        signaling = nil
    }

    // MARK: Signaling lifecycle

    private func openSocket() {
        guard !stopped else { return }
        connection = .connecting
        onConnectionChange?(connection)

        // wss://host/api/ws?token=... — the server's WSS endpoint.
        var components = URLComponents(url: config.baseURL, resolvingAgainstBaseURL: false)
        components?.scheme = config.baseURL.scheme == "https" ? "wss" : "ws"
        components?.path = "/api/ws"

        let client = SignalingClient(
            url: components?.url ?? config.baseURL,
            token: config.token,
            queue: signalingQueue
        )
        signaling = client

        client.onConnected = { [weak self] in
            Task { @MainActor in self?.handleConnected() }
        }
        client.onDisconnect = { [weak self] reason in
            Task { @MainActor in self?.handleDisconnected(reason: reason) }
        }
        client.onFrame = { [weak self] result in
            Task { @MainActor in self?.handleFrameResult(result) }
        }
        client.connect()
    }

    private func handleConnected() {
        reconnect.recordConnection()
        connection = .connected
        onConnectionChange?(connection)
    }

    private func handleDisconnected(reason: String) {
        guard !stopped else { return }
        connection = .disconnected(reason: reason)
        onConnectionChange?(connection)

        // The wire drop also ends the local floor; the server released the
        // burst (its loss-of-connectivity rule).
        if case .granted = floor {
            reduceFloor(.pttReleased)
        }
        reconnect.recordDrop()
        scheduleReconnect()
    }

    /// Backoff-scheduled reconnect: delay = min(cap, base·2^n) with jitter.
    private func scheduleReconnect() {
        guard reconnectTask == nil else { return }
        let attempt = reconnect.nextAttempt
        let delay = backoff.jitteredDelay(
            forAttempt: attempt,
            random: Double.random(in: 0..<1)
        )
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !self.stopped else { return }
            self.reconnectTask = nil
            self.openSocket()
        }
    }

    // MARK: Wire -> stores

    private func handleFrameResult(_ result: Result<InboundMessage, MessageCodecError>) {
        switch result {
        case .success(let message):
            handleFrame(message)
        case .failure(let error):
            // Malformed payloads are logged, never silently dropped; the
            // connection survives (server parity: malformed frames are
            // dropped, the pump continues).
            NSLog("mcptt: frame decode failed: \(error)")
        }
    }

    private func handleFrame(_ message: InboundMessage) {
        let now = Date()
        calls.reduce(message, myUserId: config.userId, now: now)
        roster.reduce(message, now: now)

        switch message {
        case .floorGranted(let grant):
            guard grant.callId == calls.activeCall?.id else { return }
            if grant.userId == config.userId {
                reduceFloor(.granted(at: now.timeIntervalSince1970))
            }
            ringingCall = nil

        case .floorDenied(let denial):
            guard denial.callId == calls.activeCall?.id,
                  denial.userId == config.userId else { return }
            reduceFloor(.denied(reason: denial.reason, queuePosition: denial.queuePosition))

        case .floorPreempted(let preempt):
            // The fan-out names who TOOK the floor, not who was stripped —
            // the holder is whoever's state was granted. Reduce only when
            // that is us; everyone else renders the new speaker from the
            // CallStore update that already ran.
            guard preempt.callId == calls.activeCall?.id,
                  isMyBurstActive(),
                  preempt.by != config.userId else { return }
            reduceFloor(.preempted(
                by: preempt.by,
                emergency: preempt.emergency,
                pttHeld: pttHeld
            ))

        case .callStarted:
            ringingCall = privateCallRingingForMe()
            if ringingCall == nil, let call = calls.activeCall {
                // Group call announcements auto-join (late entry contract).
                joinCall(call.id)
            }

        case .callEnded:
            reduceFloor(.callEnded)
            ringingCall = nil

        case .emergencyAlert:
            // The fan-out refreshes the REST list (authoritative); the
            // voiceless alert and its location land on the rail.
            Task { await refreshAlerts() }

        case .emergencyAlertAck(let ack):
            emergencyController?.serverAck(alertId: ack.alertId)
            alerts.removeAll { $0.id == ack.alertId }
            Task { await refreshAlerts() }

        case .mediaAnswer(let answer):
            media?.handleAnswer(callId: answer.callId, sdp: answer.sdp, err: answer.err)

        case .unknown, .presenceUpdate, .affiliationChanged, .floorReleased,
             .floorRevoke, .floorRequest, .callJoined, .participantRemoved:
            break
        }
    }

    private func privateCallRingingForMe() -> ActiveCall? {
        guard let call = calls.calls.last, call.kind == .privateCall,
              call.calleeId == config.userId,
              !calls.joinedCallIds.contains(call.id) else { return nil }
        return call
    }

    private func isMyBurstActive() -> Bool {
        if case .granted = floor { return true }
        return false
    }

    // MARK: Floor reducer execution

    private func reduceFloor(_ event: FloorEvent) {
        let transition = FloorReducer.reduce(floor, event)
        floor = transition.state
        for effect in transition.effects {
            execute(effect)
        }
    }

    private func execute(_ effect: FloorEffect) {
        switch effect {
        case .sendFloorRequest:
            guard let call = calls.activeCall else { return }
            signaling?.send(.floorRequest(FloorRequestMessage(
                callId: call.id,
                userId: config.userId,
                priority: config.priority,
                emergency: call.emergency
            )))
        case .sendFloorRelease:
            guard let call = calls.activeCall else { return }
            signaling?.send(.floorReleased(FloorReleasedMessage(
                callId: call.id,
                userId: config.userId
            )))
        case .startTransmitting:
            media?.setMicLive(true)
            audio?.apply(route: .speaker)
        case .stopTransmitting:
            media?.setMicLive(false)
        case .startBurstTimer(let startedAt, let duration):
            startBurstTimer(startedAt: startedAt, duration: duration)
        case .cancelBurstTimer:
            burstTimer?.cancel()
            burstTimer = nil
            burstRemaining = 0
        case .showDeniedToast(let reason):
            deniedToast = reason
        case .dismissDeniedToast:
            deniedToast = nil
        case .showPreemptBanner(let by, _):
            preemptBanner = by
        case .dismissPreemptBanner:
            preemptBanner = nil
        }
    }

    private func startBurstTimer(startedAt: TimeInterval, duration: TimeInterval) {
        burstTimer?.cancel()
        burstTimer = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                let elapsed = Date().timeIntervalSince1970 - startedAt
                let remaining = max(0, duration - elapsed)
                self.burstRemaining = remaining
                if remaining <= 0 {
                    self.reduceFloor(.burstExpired)
                    return
                }
                try? await Task.sleep(nanoseconds: 250_000_000)
            }
        }
    }

    // MARK: PTT and call actions (the screens call these)

    func pttPressed() {
        pttHeld = true
        reduceFloor(.pttPressed)
    }

    func pttReleased() {
        pttHeld = false
        reduceFloor(.pttReleased)
    }

    func denyToastAcknowledged() {
        reduceFloor(.denyAcknowledged)
    }

    func startGroupCall(groupId: String) {
        // The server mints callIds; this provisional one rides the frame
        // until its server fan-out (server ingest of client frames is a
        // documented gap — the announcement arrives as callStarted).
        signaling?.send(.callStart(CallStartMessage(
            callId: "local-" + UUID().uuidString,
            groupId: groupId,
            kind: .group,
            initiatorId: config.userId
        )))
    }

    func startPrivateCall(calleeId: String) {
        // calleeId is optimistic: not in the pinned contract yet, carried so
        // the frame is right the day the server begins ingesting it.
        signaling?.send(.callStart(CallStartMessage(
            callId: "local-" + UUID().uuidString,
            groupId: nil,
            kind: .privateCall,
            initiatorId: config.userId,
            calleeId: calleeId
        )))
    }

    /// Join is media join: open the client's audio leg (offer) into the
    /// call. There is no join frame on the wire — participation propagates
    /// through the server's CallJoined fan-out.
    func joinCall(_ callId: String) {
        calls.markJoined(callId: callId)
        ringingCall = nil
        media?.join(callId: callId)
    }

    func declineCall(_ callId: String) {
        // No decline frame exists; declining is local — the missed-call
        // entry records it and the ring stops.
        calls.markDeclined(
            callId: callId,
            myUserId: config.userId,
            now: Date()
        )
        ringingCall = nil
    }

    /// Local teardown of this user's participation. There is no field-user
    /// end-call REST route (ending is a dispatcher action); the server ends
    /// the call when the last participant leaves.
    func leaveCall() {
        guard let call = calls.activeCall else { return }
        reduceFloor(.callEnded)
        calls.leave(callId: call.id)
        media?.close()
    }

    func refreshAlerts() async {
        do {
            alerts = try await api.fetchAlerts(token: config.token)
        } catch {
            NSLog("mcptt: alert refresh failed: \(error)")
        }
    }

    // Installed by AppModel at login (extensions cannot hold stored state).
    var emergencyController: EmergencyController?
    var audio: AudioSessionController?
}

// MARK: - Emergency wiring

extension SessionController {
    /// Called from the confirmation sheet via EmergencyHoldModel. Fires the
    /// alert (retry-queued) and optionally the emergency call whose
    /// server-side decision pre-empts the active floor.
    func emergencyConfirm(groupId: String?, withCall: Bool, imminentPeril: Bool) async {
        guard let emergencyController else { return }
        await emergencyController.confirm(
            groupId: groupId,
            callId: withCall ? calls.activeCall?.id : nil,
            withCall: withCall,
            imminentPeril: imminentPeril
        )
    }

    /// Installed by AppModel at login; nil until then.
    func attach(emergency: EmergencyController, audio: AudioSessionController) {
        emergencyController = emergency
        self.audio = audio
        audio.activateForCall()
    }

    /// The retry-queue surfaces in the Alerts screen badge.
    var pendingAlerts: Int {
        emergencyController?.pendingCount ?? 0
    }
}
