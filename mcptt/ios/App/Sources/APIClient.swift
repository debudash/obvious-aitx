// REST access — only routes that exist on the server today:
//   POST /api/auth/login, GET /api/users, GET /api/groups,
//   GET /api/affiliations, GET /api/emergency/alerts,
//   POST /api/calls/emergency, POST /api/emergency/alerts.
// No bootstrap endpoint exists; bootstrap is the three GETs composed.

import Foundation
import MCPTTCore

/// The three GETs that seed the roster at login.
struct RosterBootstrap: Sendable {
    var users: [UserDTO]
    var groups: [GroupDTO]
    var affiliations: [AffiliationDTO]
}

struct APIError: Error, LocalizedError {
    let status: Int
    let message: String

    var errorDescription: String? { message }
}

final class APIClient {
    private let baseURL: URL
    private let session: URLSession

    init(baseURL: URL, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.session = session
    }

    // MARK: Auth

    func login(_ credentials: Credentials) async throws -> LoginResponse {
        try await post(
            path: "/api/auth/login",
            body: LoginRequest(username: credentials.username, password: credentials.password)
        )
    }

    // MARK: Roster bootstrap

    func fetchBootstrap(token: String) async throws -> RosterBootstrap {
        async let users = get([UserDTO].self, path: "/api/users", token: token)
        async let groups = get([GroupDTO].self, path: "/api/groups", token: token)
        async let affiliations = get([AffiliationDTO].self, path: "/api/affiliations", token: token)
        return try await RosterBootstrap(
            users: users, groups: groups, affiliations: affiliations
        )
    }

    // MARK: Emergency (REST is the live surface; WSS ingest is not wired)

    func emergencyCall(_ body: EmergencyCallRequest, token: String) async throws -> EmergencyCallResponse {
        try await post(path: "/api/calls/emergency", body: body, token: token)
    }

    func raiseAlert(_ body: EmergencyCallRequest, token: String) async throws -> AlertDTO {
        try await post(path: "/api/emergency/alerts", body: body, token: token)
    }

    /// Field users receive only their own history (the Alerts screen's
    /// data source); dispatchers get everything.
    func fetchAlerts(token: String) async throws -> [AlertDTO] {
        try await get([AlertDTO].self, path: "/api/emergency/alerts", token: token)
    }

    // MARK: Transport

    private func request(path: String, token: String?, method: String, body: Data?) -> URLRequest {
        var request = URLRequest(url: baseURL.appendingPathComponent(path))
        request.httpMethod = method
        if let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    private func post<Body: Encodable, Response: Decodable>(
        path: String, body: Body, token: String? = nil
    ) async throws -> Response {
        let data = try JSONEncoder().encode(body)
        var request = request(path: path, token: token, method: "POST", body: data)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        return try await run(request)
    }

    private func get<Response: Decodable>(_: Response.Type, path: String, token: String) async throws -> Response {
        try await run(request(path: path, token: token, method: "GET", body: nil))
    }

    private func run<Response: Decodable>(_ request: URLRequest) async throws -> Response {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError(status: 0, message: "non-HTTP response")
        }
        guard (200..<300).contains(http.statusCode) else {
            let body = try? JSONDecoder().decode(APIErrorBody.self, from: data)
            throw APIError(status: http.statusCode, message: body?.error ?? "HTTP \(http.statusCode)")
        }
        return try JSONDecoder().decode(Response.self, from: data)
    }
}
