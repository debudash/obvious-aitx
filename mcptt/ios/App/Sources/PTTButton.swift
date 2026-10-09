// The PTT button — the floor indicator. States mirror FloorReducer exactly:
// idle (ready), requesting (in flight), granted (transmitting + burst
// countdown), queued (#N to speak), denied (toast lives on the screen),
// preempted (banner on the screen; button back to ready).
//
// The guarded emergency gesture: press-and-hold 1.5 s arms the confirmation
// sheet; confirming fires the alert (and optional emergency call).

import SwiftUI
import MCPTTCore

struct PTTButton: View {
    @ObservedObject var controller: SessionController
    @StateObject private var emergency: EmergencyHoldModel
    @State private var showEmergencyConfirm = false

    init(controller: SessionController) {
        self.controller = controller
        _emergency = StateObject(wrappedValue: EmergencyHoldModel(controller: controller))
    }

    var body: some View {
        VStack(spacing: 10) {
            button
            if case .granted = controller.floor {
                Text(String(format: "Burst ends in %02d:%02d",
                            Int(controller.burstRemaining) / 60,
                            Int(controller.burstRemaining) % 60))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
        .sheet(isPresented: $showEmergencyConfirm) {
            EmergencyConfirmSheet(
                onConfirm: { withCall, peril in
                    showEmergencyConfirm = false
                    Task { await emergency.confirm(groupId: controller.calls.activeCall?.groupId,
                                                   withCall: withCall,
                                                   imminentPeril: peril) }
                },
                onCancel: {
                    showEmergencyConfirm = false
                    emergency.cancel()
                }
            )
        }
        .onChange(of: emergency.armed) { armed in
            if armed {
                emergency.armed = false
                showEmergencyConfirm = true
            }
        }
    }

    @ViewBuilder
    private var button: some View {
        let label = label(for: controller.floor)
        let color = color(for: controller.floor)

        Circle()
            .fill(color)
            .frame(width: 148, height: 148)
            .overlay(
                VStack(spacing: 4) {
                    Image(systemName: icon(for: controller.floor))
                        .font(.system(size: 34, weight: .bold))
                    label
                        .font(.caption.weight(.bold))
                        .multilineTextAlignment(.center)
                }
                .foregroundStyle(.white)
            )
            .shadow(color: color.opacity(0.5), radius: controller.floor.isTransmitting ? 24 : 6)
            .gesture(emergencyGesture)
            .accessibilityLabel(label(for: controller.floor))
    }

    /// One gesture drives both paths: PTT fires on any press; the emergency
    /// timer arms at 1.5 s of continuous hold. A plain PTT press releases
    /// well before that.
    private var emergencyGesture: some Gesture {
        DragGesture(minimumDistance: 0)
            .onChanged { _ in
                if !controller.floor.isHeld {
                    controller.pttPressed()
                    emergency.pressBegan()
                }
            }
            .onEnded { _ in
                controller.pttReleased()
                emergency.pressEnded()
            }
    }

    private func label(for state: FloorState) -> Text {
        switch state {
        case .idle: return Text("HOLD TO TALK")
        case .requesting: return Text("REQUESTING…")
        case .granted: return Text("TRANSMITTING")
        case .queued(let position): return Text("#\(position) TO SPEAK")
        case .denied: return Text("DENIED")
        case .preempted: return Text("PRE-EMPTED")
        }
    }

    private func color(for state: FloorState) -> Color {
        switch state {
        case .idle: return .blue
        case .requesting: return .orange
        case .granted: return .green
        case .queued: return .orange
        case .denied: return .red
        case .preempted: return .red
        }
    }

    private func icon(for state: FloorState) -> String {
        switch state {
        case .idle: return "mic.fill"
        case .requesting: return "hourglass"
        case .granted: return "mic.fill"
        case .queued: return "clock"
        case .denied: return "hand.raised.fill"
        case .preempted: return "exclamationmark.triangle.fill"
        }
    }
}

/// The 1.5 s emergency hold detection, isolated so the gesture stays simple.
@MainActor
final class EmergencyHoldModel: ObservableObject {
    @Published var armed = false
    private var holdTask: Task<Void, Never>?
    private weak var controller: SessionController?

    init(controller: SessionController) {
        self.controller = controller
    }

    func pressBegan() {
        holdTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            guard let self, !Task.isCancelled else { return }
            self.armed = true
        }
    }

    func pressEnded() {
        holdTask?.cancel()
        holdTask = nil
    }

    func confirm(groupId: String?, withCall: Bool, imminentPeril: Bool) async {
        await controller?.emergencyConfirm(groupId: groupId, withCall: withCall, imminentPeril: imminentPeril)
    }

    func cancel() {
        armed = false
    }
}

struct EmergencyConfirmSheet: View {
    let onConfirm: (_ withCall: Bool, _ imminentPeril: Bool) -> Void
    let onCancel: () -> Void

    var body: some View {
        VStack(spacing: 20) {
            Text("EMERGENCY")
                .font(.title.bold())
                .foregroundStyle(.red)
            Text("Fire an emergency alert with your location?")
                .multilineTextAlignment(.center)
            Button {
                onConfirm(true, false)
            } label: {
                Label("Alert + emergency call", systemImage: "phone.fill")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            Button {
                onConfirm(false, false)
            } label: {
                Label("Alert only", systemImage: "exclamationmark.triangle")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            Button("Cancel", role: .cancel) { onCancel() }
        }
        .padding(24)
        .presentationDetents([.medium])
    }
}
