// Contacts — roster with private-call dialing and missed-call entries
// (declined or unanswered private calls appear here per the spec).

import SwiftUI
import MCPTTCore

struct ContactsScreen: View {
    @EnvironmentObject private var appModel: AppModel

    var body: some View {
        if let controller = appModel.session?.controller {
            ContactsScreenContent(controller: controller)
        }
    }
}

private struct ContactsScreenContent: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        List {
            if !controller.calls.missedCalls.isEmpty {
                Section("Missed calls") {
                    ForEach(controller.calls.missedCalls) { missed in
                        Label {
                            VStack(alignment: .leading) {
                                Text(controller.roster.displayName(for: missed.fromUserId))
                                Text("\(missed.kind == .declined ? "Declined" : "Missed") · \(missed.at, style: .time)")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        } icon: {
                            Image(systemName: "phone.arrow.down.left")
                                .foregroundStyle(.red)
                        }
                    }
                }
            }
            Section("People") {
                ForEach(controller.roster.users) { user in
                    HStack {
                        VStack(alignment: .leading) {
                            Text(user.functionalAlias ?? user.displayName)
                            Text("\(user.role) · P\(user.priority)")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        if user.id != controller.config.userId {
                            Button("Call") { controller.startPrivateCall(calleeId: user.id) }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                        }
                    }
                }
            }
        }
    }
}
