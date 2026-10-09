// MCPTT field client — app entry point.
//
// Compiled only on Apple platforms (Xcode / swift build on macOS). The
// platform-independent core lives in MCPTTCore and is tested on Linux; this
// target is delivered as compiling source per the spec's build-constraint
// note — no macOS toolchain exists in the delivery sandbox, so Xcode
// assembly is a documented follow-up.

import SwiftUI

@main
struct MCPTTApp: App {
    @StateObject private var appModel: AppModel

    init() {
        _appModel = StateObject(wrappedValue: AppModel())
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(appModel)
                .preferredColorScheme(.dark)
        }
    }
}

/// The five screens of the shared contract: Login, Talk, Contacts, Alerts,
/// Settings. RootView switches to the tab shell after login.
struct RootView: View {
    @EnvironmentObject private var appModel: AppModel

    var body: some View {
        if appModel.session != nil {
            TabView {
                TalkScreen()
                    .tabItem { Label("Talk", systemImage: "mic.fill") }
                ContactsScreen()
                    .tabItem { Label("Contacts", systemImage: "person.2") }
                AlertsScreen()
                    .tabItem { Label("Alerts", systemImage: "exclamationmark.triangle") }
                SettingsScreen()
                    .tabItem { Label("Settings", systemImage: "gearshape") }
            }
        } else {
            LoginScreen()
        }
    }
}
