// Alerts — the user's own emergency history plus the retry queue state.

import SwiftUI
import MCPTTCore

struct AlertsScreen: View {
    @EnvironmentObject private var appModel: AppModel

    var body: some View {
        if let controller = appModel.session?.controller {
            AlertsScreenContent(controller: controller)
        }
    }
}

private struct AlertsScreenContent: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        List {
            if controller.pendingAlerts > 0 {
                Section("Sending") {
                    Label {
                        Text("Emergency alert queued — retrying until dispatch acknowledges")
                    } icon: {
                        ProgressView()
                    }
                }
            }
            Section("History") {
                if controller.alerts.isEmpty {
                    Text("No alerts yet")
                        .foregroundStyle(.secondary)
                }
                ForEach(controller.alerts.reversed()) { alert in
                    VStack(alignment: .leading) {
                        HStack {
                            Text(alert.isImminentPeril ? "Imminent peril" : "Emergency")
                                .font(.subheadline.weight(.semibold))
                            Spacer()
                            Text(alert.createdAt, style: .relative)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        if let lat = alert.lat, let lon = alert.lon {
                            Text(String(format: "Location %.4f, %.4f", lat, lon))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Text(alert.isActive ? "Active — awaiting acknowledgement" : "Acknowledged")
                            .font(.caption)
                            .foregroundStyle(alert.isActive ? .orange : .green)
                    }
                }
            }
        }
    }
}
