// RosterStore reduces the identity fan-out: users, groups, affiliations,
// presence. All inputs are server fan-out messages or bootstrap snapshots;
// the store never invents state.

import Foundation

/// Users, groups, affiliations, and presence as the roster screens render
/// them. Pure reducer — no I/O.
public struct RosterStore: Sendable {
    public private(set) var users: [UserDTO] = []
    public private(set) var groups: [GroupDTO] = []
    public private(set) var affiliations: [AffiliationDTO] = []
    /// userId → "online" | "offline" (PresenceUpdate.state).
    public private(set) var presence: [String: String] = [:]

    public init() {}

    // MARK: Bootstrap snapshots (REST)

    public mutating func set(users: [UserDTO]) {
        self.users = users
    }

    public mutating func set(groups: [GroupDTO]) {
        self.groups = groups
    }

    public mutating func set(affiliations: [AffiliationDTO]) {
        self.affiliations = affiliations
    }

    // MARK: WSS fan-out

    public mutating func reduce(_ message: InboundMessage, now: Date) {
        switch message {
        case let .presenceUpdate(update):
            presence[update.userId] = update.state
            ensureUserStub(userId: update.userId)
        case let .affiliationChanged(changed):
            let dto = AffiliationDTO(
                userId: changed.userId,
                groupId: changed.groupId,
                state: changed.state,
                changedAt: Date(timeIntervalSince1970: Double(changed.at) / 1000.0)
            )
            affiliations.removeAll {
                $0.userId == changed.userId && $0.groupId == changed.groupId
            }
            if changed.state == AffiliationState.affiliated {
                affiliations.append(dto)
            }
            ensureUserStub(userId: changed.userId)
        default:
            break
        }
    }

    /// A presence or affiliation fan-out for a user we have not bootstrapped
    /// yet still earns a stub so displays never show raw IDs.
    public mutating func ensureUserStub(userId: String) {
        guard !users.contains(where: { $0.id == userId }) else { return }
        users.append(UserDTO(
            id: userId, username: userId, displayName: userId,
            role: "field", priority: 4, functionalAlias: nil, createdAt: Date()
        ))
    }

    // MARK: Queries

    public func displayName(for userId: String) -> String {
        users.first(where: { $0.id == userId })?.displayName ?? userId
    }

    public func user(id: String) -> UserDTO? {
        users.first(where: { $0.id == id })
    }

    public func groupsForUser(_ userId: String) -> [GroupDTO] {
        let groupIds = Set(affiliations
            .filter { $0.userId == userId && $0.state == AffiliationState.affiliated }
            .map { $0.groupId })
        return groups.filter { groupIds.contains($0.id) }
    }

    public func affiliatedGroups(userId: String) -> [GroupDTO] {
        groupsForUser(userId)
    }
}
