import SwiftUI

struct LoginView: View {
    @ObservedObject var model: AppModel
    @State private var serverSettingsOpen = false
    private var checkingServer: Bool { model.serverCheck == .checking }
    @FocusState private var focusedField: Field?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    private enum Field { case username, password, server }

    init(model: AppModel, serverSettingsOpen: Bool = false) {
        self.model = model
        _serverSettingsOpen = State(initialValue: serverSettingsOpen)
    }

    var body: some View {
        GeometryReader { geometry in
            ScrollView {
                VStack(spacing: 28) {
                    WorkspaceBrandLockup()
                    VStack(alignment: .leading, spacing: 18) {
                        if let vmid = model.pendingConnectionVMID {
                            Label("登录后将打开 VM \(String(vmid))", systemImage: "arrow.up.forward.app")
                                .font(.callout).foregroundStyle(.secondary)
                        }
                        entry("用户名", field: .username) {
                            TextField("用户名", text: $model.username, prompt: Text(""))
                                .textContentType(.username)
                                .onSubmit { focusedField = .password }
                        }
                        entry("密码", field: .password) {
                            SecureField("密码", text: $model.password, prompt: Text(""))
                                .textContentType(.password)
                                .onSubmit { submitLogin() }
                        }
                        Button(action: submitLogin) {
                            Text("登录").frame(maxWidth: .infinity)
                        }
                        .buttonStyle(WorkspaceActionStyle())
                        .disabled(model.username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || model.password.isEmpty || checkingServer)

                        if model.system?.oidcConfigured == true {
                            Button { model.loginWithOIDC() } label: {
                                Text("使用 \(model.system?.oidcName ?? "SSO") 登录")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(WorkspaceActionStyle(emphasis: .secondary))
                            .disabled(checkingServer)
                        }
                        DisclosureGroup("服务器设置", isExpanded: $serverSettingsOpen) {
                            VStack(alignment: .leading, spacing: 12) {
                                entry("控制面地址", field: .server) {
                                    TextField("控制面地址", text: $model.server, prompt: Text("https://workspace.example.com"))
                                        .textContentType(.URL)
                                        .onSubmit { checkServer() }
                                }
                                Button(checkingServer ? "正在检查连接" : "检查连接", action: checkServer)
                                    .buttonStyle(.borderless)
                                    .disabled(checkingServer || model.server.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                                ServerConnectionFeedback(state: model.serverCheck)
                            }
                            .padding(.top, 14)
                        }
                        .font(.system(size: 13))
                        .foregroundStyle(.secondary)
                        .tint(.secondary)

                        if !model.errorMessage.isEmpty {
                            Label(model.errorMessage, systemImage: "exclamationmark.circle")
                                .font(.callout).foregroundStyle(Color(nsColor: .systemRed))
                                .fixedSize(horizontal: false, vertical: true)
                                .transition(.opacity)
                        }
                    }
                    .frame(width: 336)
                }
                .padding(WorkspaceStyle.pageInset)
                .frame(maxWidth: .infinity, minHeight: geometry.size.height)
            }
            .background { WorkspaceSceneBackground() }
        }
        .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: serverSettingsOpen)
        .onAppear { focusedField = model.username.isEmpty ? .username : .password }
    }

    private func entry<Content: View>(_ title: String, field: Field, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            Text(title).font(.system(size: 13, weight: .medium))
                .onTapGesture { focusedField = field }
                .accessibilityHidden(true)
            content()
                .accessibilityLabel(title)
                .textFieldStyle(.plain)
                .font(.system(size: 14))
                .focused($focusedField, equals: field)
                .padding(.horizontal, 12)
                .frame(height: WorkspaceStyle.controlHeight)
                .background(WorkspaceStyle.surface, in: RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius))
                .overlay {
                    RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius)
                        .strokeBorder(focusedField == field ? Color.accentColor : WorkspaceStyle.line.opacity(0.75), lineWidth: focusedField == field ? 2 : 1)
                        .allowsHitTesting(false)
                }
                .animation(.easeOut(duration: 0.12), value: focusedField)
        }
    }

    private func submitLogin() {
        guard !checkingServer, model.state == .signedOut,
              !model.username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !model.password.isEmpty else { return }
        Task { await model.login() }
    }

    private func checkServer() {
        Task { await model.checkServerConnection() }
    }
}

struct ServerConnectionFeedback: View {
    let state: AppModel.ServerCheck

    var body: some View {
        Group {
            switch state {
            case .idle, .checking:
                EmptyView()
            case .succeeded:
                Label("服务器连接正常", systemImage: "checkmark.circle")
                    .foregroundStyle(.secondary)
            case .failed(let message):
                Label(message, systemImage: "exclamationmark.circle")
                    .foregroundStyle(Color(nsColor: .systemRed))
            }
        }
        .font(.callout)
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityElement(children: .combine)
    }
}
