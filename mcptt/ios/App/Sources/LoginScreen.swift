// Login — server URL + credentials per the spec's screen list.

import SwiftUI

struct LoginScreen: View {
    @EnvironmentObject private var appModel: AppModel
    @State private var serverURL = ""
    @State private var username = ""
    @State private var password = ""

    var body: some View {
        VStack(spacing: 16) {
            VStack(alignment: .leading, spacing: 4) {
                Text("MCPTT")
                    .font(.largeTitle.bold())
                Text("Mission-critical push-to-talk")
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, 8)

            TextField("Server URL", text: $serverURL)
                .textContentType(.URL)
                .keyboardType(.URL)
                .autocorrectionDisabled()
                .textFieldStyle(.roundedBorder)
            TextField("Username", text: $username)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .textFieldStyle(.roundedBorder)
            SecureField("Password", text: $password)
                .textFieldStyle(.roundedBorder)

            if let error = appModel.loginError {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(.red)
            }

            Button {
                appModel.login(Credentials(
                    serverURL: serverURL,
                    username: username,
                    password: password
                ))
            } label: {
                if appModel.isLoggingIn {
                    ProgressView()
                        .frame(maxWidth: .infinity)
                } else {
                    Text("Sign in")
                        .frame(maxWidth: .infinity)
                }
            }
            .buttonStyle(.borderedProminent)
            .disabled(appModel.isLoggingIn || username.isEmpty || password.isEmpty || serverURL.isEmpty)

            Spacer()
        }
        .padding(24)
    }
}
