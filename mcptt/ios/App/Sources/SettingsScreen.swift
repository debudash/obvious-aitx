// Settings — affiliations, functional alias, priority display; logout.

import SwiftUI
import MCPTTCore

struct SettingsScreen: View {
    @EnvironmentObject private var appModel: AppModel

    var body: some View {
        if let session = appModel.session {
            SettingsScreenContent(
                controller: session.controller,
                user: session.user,
                onLogout: { appModel.logout() }
            )
        }
    }
}

private struct SettingsScreenContent: View {
    @ObservedObject var controller: SessionController
    let user: UserDTO
    let onLogout: () -> Void

    var body: some View {
        List {
            Section("Identity") {
                LabeledContent("User", value: user.functionalAlias ?? user.displayName)
                LabeledContent("Role", value: user.role)
                LabeledContent("Priority", value: "P\(user.priority)")
            }
            Section("Affiliated groups") {
                ForEach(controller.roster.affiliatedGroups(userId: controller.config.userId)) { group in
                    LabeledContent(group.name, value: "affiliated")
                }
            }
            Section {
                Button("Sign out", role: .destructive) { onLogout() }
            }
        }
    }
}
