// Audio policy — the AVAudioSession configuration the spec's client table
// prescribes: play-and-record, voice-call mode, speakerphone while
// transmitting, background audio entitlement (Info.plist side).
//
// The plan itself is pure data so the policy is unit-testable without an
// audio session; AudioSessionController (app layer) applies it on device.

import Foundation

/// The audio routes the Talk screen cycles through.
public enum AudioRoute: Equatable, Sendable {
    /// Listening — receiver-friendly, quiet.
    case earpiece
    /// Transmitting or a speaker call — loud, hands-free.
    case speaker
}

public struct AudioSessionPlan: Equatable, Sendable {
    public var route: AudioRoute

    public init(route: AudioRoute = .earpiece) {
        self.route = route
    }

    /// The configuration to apply for the current route. Category and mode
    /// are carried as the raw AVFAudio string values so the policy is
    /// testable on Linux's Foundation-only core; AudioSessionController
    /// maps them to `AVAudioSession.Category.playAndRecord` and
    /// `.Mode.voiceChat` on Apple platforms.
    public var configuration: AudioSessionConfiguration {
        AudioSessionConfiguration(
            category: "playAndRecord",
            mode: "voiceChat",
            routeOverride: route == .speaker ? "speaker" : "none"
        )
    }
}

public struct AudioSessionConfiguration: Equatable, Sendable {
    public let category: String
    public let mode: String
    public let routeOverride: String

    public init(category: String, mode: String, routeOverride: String) {
        self.category = category
        self.mode = mode
        self.routeOverride = routeOverride
    }
}
