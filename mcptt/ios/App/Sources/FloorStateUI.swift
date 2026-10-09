// UI-facing conveniences on FloorState — keeps the core enum display-free.

import MCPTTCore

extension FloorState {
    /// True while this client holds an open burst (green button state).
    var isTransmitting: Bool {
        if case .granted = self { return true }
        return false
    }

    /// True from press through release — used by the gesture to tell a
    /// fresh press from a drag-update.
    var isHeld: Bool {
        switch self {
        case .requesting, .granted: return true
        case .idle, .queued, .denied, .preempted: return false
        }
    }
}
