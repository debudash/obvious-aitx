// swift-tools-version:5.9
import PackageDescription

// MCPTTCore is the pure-Swift half of the iOS field client: the wire-protocol
// codecs, client-side floor state machine, call/roster stores, reconnect
// backoff, and the emergency-alert retry queue. It has no external
// dependencies and no Apple-only imports, so the protocol and state layers
// build and test on any platform with a Swift toolchain — including Linux CI
// (see .github/workflows/mcptt.yml, job ios-core).
//
// The Apple-only application sources live in App/ and are built by the Xcode
// project generated from project.yml (xcodegen), which also binds the
// stasel/WebRTC binary framework for the media plane.

let package = Package(
    name: "MCPTTCore",
    products: [
        .library(name: "MCPTTCore", targets: ["MCPTTCore"])
    ],
    targets: [
        .target(name: "MCPTTCore"),
        .testTarget(
            name: "MCPTTCoreTests",
            dependencies: ["MCPTTCore"]
        ),
    ]
)
