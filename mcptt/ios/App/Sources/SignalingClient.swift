// URLSessionWebSocketTask signaling transport. One socket, one receive
// loop; frames decode through MessageCodec (unknown types decode to
// .unknown — never dropped silently). Open-detection is a ping/pong: the
// server has no Welcome handshake frame, so a clean pong is the open signal.

import Foundation
import MCPTTCore

/// The outbound frames a field client may send, encoded via MessageCodec.
enum OutboundSignal {
    case floorRequest(FloorRequestMessage)
    case floorReleased(FloorReleasedMessage)
    case callStart(CallStartMessage)
    case emergencyAlert(EmergencyAlertMessage)
    case mediaOffer(MediaOfferMessage)

    func encode() throws -> Data {
        switch self {
        case .floorRequest(let m): return try MessageCodec.encode(m)
        case .floorReleased(let m): return try MessageCodec.encode(m)
        case .callStart(let m): return try MessageCodec.encode(m)
        case .emergencyAlert(let m): return try MessageCodec.encode(m)
        case .mediaOffer(let m): return try MessageCodec.encode(m)
        }
    }
}

final class SignalingClient: NSObject {
    var onConnected: (() -> Void)?
    var onDisconnect: ((String) -> Void)?
    /// Every decodable inbound frame, malformed ones surfaced as decode
    /// failures rather than dropped silently.
    var onFrame: ((Result<InboundMessage, MessageCodecError>) -> Void)?

    private let url: URL
    private let token: String
    private let queue: DispatchQueue
    private var session: URLSession!
    private var task: URLSessionWebSocketTask?
    private var manuallyClosed = false

    init(url: URL, token: String, queue: DispatchQueue) {
        self.url = url
        self.token = token
        self.queue = queue
        super.init()
        session = URLSession(configuration: .default, delegate: nil, delegateQueue: OperationQueue())
    }

    func connect() {
        manuallyClosed = false
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let wsTask = session.webSocketTask(with: request)
        task = wsTask
        wsTask.resume()

        // The server sends no handshake frame; a clean pong means the
        // upgrade succeeded and the socket is live.
        wsTask.sendPing { [weak self] error in
            self?.queue.async {
                guard let self else { return }
                if let error {
                    self.onDisconnect?("ping failed: \(error.localizedDescription)")
                } else {
                    self.onConnected?()
                    self.receiveLoop()
                }
            }
        }
    }

    private func receiveLoop() {
        task?.receive { [weak self] result in
            self?.queue.async {
                guard let self else { return }
                switch result {
                case .success(let message):
                    switch message {
                    case .string(let text):
                        self.onFrame?(MessageCodec.decode(Data(text.utf8)))
                    case .data(let data):
                        self.onFrame?(MessageCodec.decode(data))
                    @unknown default:
                        break
                    }
                    self.receiveLoop()
                case .failure(let error):
                    if !self.manuallyClosed {
                        self.onDisconnect?(error.localizedDescription)
                    }
                }
            }
        }
    }

    func send(_ signal: OutboundSignal) {
        guard let data = try? signal.encode(), let text = String(data: data, encoding: .utf8) else {
            return
        }
        task?.send(.string(text)) { [weak self] error in
            if let error {
                self?.queue.async { [weak self] in
                    guard let self, !self.manuallyClosed else { return }
                    self.onDisconnect?("send failed: \(error.localizedDescription)")
                }
            }
        }
    }

    func disconnect() {
        manuallyClosed = true
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
    }
}
