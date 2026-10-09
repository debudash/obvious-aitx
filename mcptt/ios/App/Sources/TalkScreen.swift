// Talk — affiliated groups, active call banner, the PTT button with live
// floor state (grant / queue position / denial / pre-emption) and the burst
// countdown. The button IS the floor indicator (spec).

import SwiftUI
import MCPTTCore

struct TalkScreen: View {
    @EnvironmentObject private var appModel: AppModel

    var body: some View {
        if let controller = appModel.session?.controller {
            TalkScreenContent(controller: controller)
        }
    }
}

private struct TalkScreenContent: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        VStack(spacing: 0) {
            ConnectionBanner(connection: controller.connection)

            ScrollView {
                VStack(spacing: 16) {
                    if let preemptBy = controller.preemptBanner {
                        PreemptBanner(by: preemptBy)
                    }
                    ActiveCallCard(controller: controller)
                    GroupsCard(controller: controller)
                }
                .padding(16)
            }

            PTTButton(controller: controller)
                .padding(.bottom, 32)
        }
        .overlay(alignment: .top) {
            if let reason = controller.deniedToast {
                DeniedToast(reason: reason) {
                    controller.denyToastAcknowledged()
                }
            }
        }
        .sheet(isPresented: Binding(
            get: { controller.ringingCall != nil },
            set: { _ in }
        )) {
            if let call = controller.ringingCall {
                RingingSheet(
                    from: controller.roster.displayName(for: call.initiatorId),
                    onAccept: { controller.joinCall(call.id) },
                    onDecline: { controller.declineCall(call.id) }
                )
            }
        }
    }
}

/// The private-call ring: accept joins (late entry), decline records the
/// missed-call entry.
struct RingingSheet: View {
    let from: String
    let onAccept: () -> Void
    let onDecline: () -> Void

    var body: some View {
        VStack(spacing: 20) {
            Label("Private call", systemImage: "phone.ring")
                .font(.headline)
            Text(from)
                .font(.title2.bold())
            HStack(spacing: 24) {
                Button {
                    onDecline()
                } label: {
                    Image(systemName: "phone.down.fill")
                        .font(.title2)
                        .padding(18)
                        .background(.red.opacity(0.2), in: Circle())
                }
                .accessibilityLabel("Decline")
                Button {
                    onAccept()
                } label: {
                    Image(systemName: "phone.fill")
                        .font(.title2)
                        .foregroundStyle(.white)
                        .padding(18)
                        .background(.green, in: Circle())
                }
                .accessibilityLabel("Accept")
            }
        }
        .padding(24)
        .presentationDetents([.medium])
    }
}

private struct ConnectionBanner: View {
    let connection: ConnectionState

    var body: some View {
        switch connection {
        case .connected:
            EmptyView()
        case .connecting:
            banner("Connecting…", color: .orange)
        case let .disconnected(reason):
            banner("Reconnecting — \(reason)", color: .red)
        case .idle:
            EmptyView()
        }
    }

    private func banner(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .foregroundStyle(.white)
            .frame(maxWidth: .infinity)
            .padding(6)
            .background(color)
    }
}

private struct PreemptBanner: View {
    let by: String

    var body: some View {
        Label("Pre-empted by \(by)", systemImage: "exclamationmark.arrow.triangle.2.circlepath")
            .font(.footnote.weight(.semibold))
            .foregroundStyle(.white)
            .padding(10)
            .frame(maxWidth: .infinity)
            .background(.red.gradient, in: RoundedRectangle(cornerRadius: 10))
    }
}

private struct ActiveCallCard: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        if let call = controller.calls.activeCall {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text(call.kind == .group ? "Group call" : "Private call")
                        .font(.headline)
                    if call.emergency {
                        Text("EMERGENCY")
                            .font(.caption.bold())
                            .foregroundStyle(.white)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(.red, in: Capsule())
                    }
                    Spacer()
                    Button("Leave") { controller.leaveCall() }
                        .buttonStyle(.bordered)
                }
                if let speaker = call.speaker {
                    Label("Speaking: \(controller.roster.displayName(for: speaker))", systemImage: "waveform")
                        .font(.subheadline)
                } else {
                    Text("Nobody speaking")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                if !call.queue.isEmpty {
                    Text("Queue: " + call.queue.map { controller.roster.displayName(for: $0) }
                        .joined(separator: ", "))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(14)
            .background(.gray.opacity(0.15), in: RoundedRectangle(cornerRadius: 12))
        } else {
            Text("No active calls")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity)
                .padding(14)
                .background(.gray.opacity(0.1), in: RoundedRectangle(cornerRadius: 12))
        }
    }
}

private struct GroupsCard: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("My groups")
                .font(.headline)
            ForEach(controller.roster.groups) { group in
                HStack {
                    VStack(alignment: .leading) {
                        Text(group.name)
                        if let desc = group.description {
                            Text(desc).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    Spacer()
                    Button("Call") { controller.startGroupCall(groupId: group.id) }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                }
                .padding(8)
                .background(.gray.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
            }
        }
    }
}

private struct DeniedToast: View {
    let reason: String
    let onAcknowledge: () -> Void

    var body: some View {
        HStack {
            Label("Denied: \(reason)", systemImage: "hand.raised")
            Spacer()
            Button("OK") { onAcknowledge() }
        }
        .font(.footnote.weight(.semibold))
        .padding(10)
        .background(.orange.opacity(0.9), in: RoundedRectangle(cornerRadius: 10))
        .padding(.horizontal, 16)
    }
}
