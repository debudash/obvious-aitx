// The app-facing state object: owns the session controller, exposes the
// stores as observable state, and mediates login/logout.

import Foundation
import Combine
import MCPTTCore

/// Session credentials as entered on Login.
struct Credentials: Equatable {
    var serverURL: String
    var username: String
    var password: String
}

@MainActor
final class AppModel: ObservableObject {
    @Published private(set) var session: Session?
    @Published private(set) var loginError: String?
    @Published private(set) var isLoggingIn = false

    /// The session's live state, re-published from the controller.
    @Published private(set) var connectionState: ConnectionState = .idle

    private var controller: SessionController?
    private var api: APIClient?

    func login(_ credentials: Credentials) {
        guard !isLoggingIn else { return }
        isLoggingIn = true
        loginError = nil

        Task { [weak self] in
            guard let self else { return }
            do {
                guard let base = URL(string: credentials.serverURL) else {
                    await MainActor.run {
                        self.loginError = "Invalid server URL"
                        self.isLoggingIn = false
                    }
                    return
                }
                let api = APIClient(baseURL: base)
                let response = try await api.login(
                    LoginRequest(username: credentials.username, password: credentials.password)
                )
                let bootstrap = try await api.fetchBootstrap(token: response.token)

                let controller = SessionController(
                    api: api,
                    config: SessionConfig(
                        baseURL: base,
                        token: response.token,
                        userId: response.user.id,
                        priority: response.user.priority
                    ),
                    initial: bootstrap
                )
                self.api = api
                self.controller = controller
                self.session = Session(
                    user: response.user,
                    token: response.token,
                    controller: controller
                )
                self.observe(controller)
                controller.attach(
                    emergency: EmergencyController(api: api, token: response.token),
                    audio: AudioSessionController()
                )
                controller.start()
            } catch {
                self.loginError = Self.describe(error)
            }
            self.isLoggingIn = false
        }
    }

    func logout() {
        controller?.stop()
        controller = nil
        api = nil
        session = nil
        connectionState = .idle
    }

    private func observe(_ controller: SessionController) {
        controller.onConnectionChange = { [weak self] state in
            Task { @MainActor in self?.connectionState = state }
        }
    }

    private static func describe(_ error: Error) -> String {
        if let apiError = error as? APIError {
            return apiError.message
        }
        return error.localizedDescription
    }
}

/// What RootView switches on.
struct Session {
    let user: UserDTO
    let token: String
    let controller: SessionController
}
