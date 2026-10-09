// Applies the AudioSessionPlan to the real AVAudioSession and owns audio
// focus while transmitting (the spec's audio-policy row). Apple-only.

import Foundation
import AVFoundation
import MCPTTCore

final class AudioSessionController {
    private var plan = AudioSessionPlan()

    /// Configures play-and-record + voice-chat and activates the session.
    /// Called once per call session (join).
    func activateForCall() {
        let session = AVAudioSession.sharedInstance()
        do {
            try session.setCategory(
                .playAndRecord,
                mode: .voiceChat,
                options: [.allowBluetooth, .defaultToSpeaker]
            )
            try session.setActive(true)
        } catch {
            // Mission-critical: a failed audio configuration must be visible,
            // not silent. The call can still proceed with defaults.
            NSLog("MCPTT audio session configuration failed: \(error)")
        }
    }

    /// Route switch driven by the PTT state: speaker while transmitting.
    func apply(route: AudioRoute) {
        plan.route = route
        let session = AVAudioSession.sharedInstance()
        do {
            try session.overrideOutputAudioPort(
                route == .speaker ? .speaker : .none
            )
        } catch {
            NSLog("MCPTT audio route override failed: \(error)")
        }
    }

    /// Deactivates on call end so other apps' audio resumes.
    func deactivate() {
        try? AVAudioSession.sharedInstance().setActive(
            false,
            options: .notifyOthersOnDeactivation
        )
    }
}
