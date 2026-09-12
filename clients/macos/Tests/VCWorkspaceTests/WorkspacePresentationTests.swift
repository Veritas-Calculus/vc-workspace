import AppKit
import SwiftUI
import Testing
@testable import VCWorkspace

@Test
func activityMotionRequiresPendingForegroundAndFullMotion() {
    for pending in [false, true] {
        for reduced in [false, true] {
            for active in [false, true] {
                #expect(WorkspaceStyle.animatesActivity(pending: pending, reduced: reduced, active: active)
                        == (pending && !reduced && active))
            }
        }
    }
}

@Test @MainActor
func nativeSurfacesStayDistinctAndPrimaryCommandsReadableInBothAppearances() throws {
    for name in [NSAppearance.Name.aqua, .darkAqua] {
        let appearance = try #require(NSAppearance(named: name))
        appearance.performAsCurrentDrawingAppearance {
            let canvas = NSColor(WorkspaceStyle.canvas).usingColorSpace(.sRGB)!
            let surface = NSColor(WorkspaceStyle.surface).usingColorSpace(.sRGB)!
            let ink = NSColor.labelColor.usingColorSpace(.sRGB)!
            #expect(abs(canvas.redComponent - surface.redComponent) > 0.02)
            func luminance(_ color: NSColor, over background: NSColor) -> CGFloat {
                let alpha = color.alphaComponent
                let channels = [
                    color.redComponent * alpha + background.redComponent * (1 - alpha),
                    color.greenComponent * alpha + background.greenComponent * (1 - alpha),
                    color.blueComponent * alpha + background.blueComponent * (1 - alpha),
                ].map { $0 <= 0.04045 ? $0 / 12.92 : pow(($0 + 0.055) / 1.055, 2.4) }
                return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
            }
            let light = luminance(surface, over: canvas)
            let dark = luminance(ink, over: surface)
            #expect((max(light, dark) + 0.05) / (min(light, dark) + 0.05) >= 4.5)
        }
    }
}

@Test @MainActor
func loginRejectsReentryAndEmptyCredentialsBeforeChangingState() async {
    let model = AppModel()
    // An invalid URL makes unintended network dispatch observable without I/O.
    model.server = ""
    model.username = "preview"
    model.password = "preview"
    model.errorMessage = "unchanged"
    for state in [AppModel.State.loading, .signedIn] {
        model.state = state
        await model.login()
        #expect(model.state == state)
        #expect(model.errorMessage == "unchanged")
    }
    model.state = .signedOut
    model.username = "  \n"
    await model.login()
    #expect(model.state == .signedOut)
    #expect(model.errorMessage == "unchanged")
    model.username = "preview"
    model.password = ""
    await model.login()
    #expect(model.errorMessage == "unchanged")
}

/// Opt-in native rendering catalogue. Uses inert fixture models: no Keychain,
/// control-plane request or remote connection; never presents/activates a window.
/// These images are review evidence, not a substitute for interactive .app QA.
@Test(.enabled(if: ProcessInfo.processInfo.environment["VC_WORKSPACE_UI_RENDER_DIR"] != nil))
@MainActor
func renderWorkspacePresentationCatalogue() async throws {
    let output = URL(fileURLWithPath: try #require(ProcessInfo.processInfo.environment["VC_WORKSPACE_UI_RENDER_DIR"]))
    try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
    let clientRoot = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()
    let oldIcon = NSApplication.shared.applicationIconImage
    NSApplication.shared.applicationIconImage = NSImage(contentsOf: clientRoot.appendingPathComponent("App/VCWorkspace.icns"))
    defer { NSApplication.shared.applicationIconImage = oldIcon }

    let desktops = [
        Desktop(vmid: 9101, name: "Debian 13 开发环境", node: "infra-node3", status: "running", cpuCount: 4, memoryTotal: 8_589_934_592),
        Desktop(vmid: 9113, name: "Windows 11 设计工作站", node: "infra-node4", status: "stopped", cpuCount: 8, memoryTotal: 17_179_869_184),
        Desktop(vmid: 9108, name: "用于验证超长名称显示边界的跨团队共享工作空间 — Design Research", node: "a-long-infrastructure-node-name-03", status: "running", cpuCount: 16, memoryTotal: 34_359_738_368),
    ]
    func fixture(_ state: AppModel.State = .signedOut) -> AppModel {
        let model = AppModel()
        model.state = state
        model.server = "https://workspace.example.com"
        return model
    }
    let login = fixture()
    let filled = fixture()
    filled.username = "preview"
    filled.password = "preview-only"
    let configured = fixture()
    configured.system = SystemState(name: "VC Workspace", oidcConfigured: true, oidcName: "Example Organization SSO")
    configured.errorMessage = "无法连接到服务器，请检查地址后重试。"
    let library = fixture(.signedIn)
    library.desktops = desktops
    let empty = fixture(.signedIn)
    let failure = fixture(.signedIn)
    failure.errorMessage = "暂时无法连接到服务器，请检查网络后重试。"
    let (checked, checkSession) = ServerConnectionCheckTests().fixture()
    let (checkFailed, failedSession) = ServerConnectionCheckTests().fixture()
    defer { checkSession.invalidateAndCancel(); failedSession.invalidateAndCancel() }
    await checked.checkServerConnection()
    checkFailed.server = "https://failed.example.test"
    await checkFailed.checkServerConnection()

    var scenes: [(String, AnyView)] = [
        ("login", AnyView(LoginView(model: login))),
        ("login-filled", AnyView(LoginView(model: filled))),
        ("login-settings-error", AnyView(LoginView(model: configured, serverSettingsOpen: true))),
        ("login-check-success", AnyView(LoginView(model: checked, serverSettingsOpen: true))),
        ("login-check-failure", AnyView(LoginView(model: checkFailed, serverSettingsOpen: true))),
        ("settings-check-success", AnyView(ClientSettingsView(model: checked).frame(maxWidth: .infinity, maxHeight: .infinity).background(WorkspaceStyle.canvas))),
        ("settings-check-failure", AnyView(ClientSettingsView(model: checkFailed).frame(maxWidth: .infinity, maxHeight: .infinity).background(WorkspaceStyle.canvas))),
        ("library", AnyView(DesktopListView(model: library, searchText: .constant("")))),
        ("library-search", AnyView(DesktopListView(model: library, searchText: .constant("Windows")))),
        ("library-search-empty", AnyView(DesktopListView(model: library, searchText: .constant("no-match")))),
        ("library-empty", AnyView(DesktopListView(model: empty, searchText: .constant("")))),
        ("library-error", AnyView(DesktopListView(model: failure, searchText: .constant("")))),
        ("display-adjusting", AnyView(SessionDisplayTransitionView())),
        ("button-states", AnyView(VStack(spacing: 16) {
            Button("连接") {}.buttonStyle(WorkspaceActionStyle())
            Button("连接") {}.buttonStyle(WorkspaceActionStyle()).disabled(true)
            Button("取消") {}.buttonStyle(WorkspaceActionStyle(emphasis: .secondary))
            Button("取消") {}.buttonStyle(WorkspaceActionStyle(emphasis: .secondary)).disabled(true)
        }.frame(maxWidth: .infinity, maxHeight: .infinity).background(WorkspaceStyle.canvas))),
    ]
    for (name, stage) in [("starting", ClientLoadingStage.starting), ("restoring", .restoringSession), ("signing-in", .signingIn), ("loading", .loadingDesktops)] {
        scenes.append((name, AnyView(WorkspaceLoadingView(stage: stage))))
    }
    for (name, stage) in [("starting", DesktopConnectionStage.starting), ("preparing", .preparing), ("opening", .opening), ("reconnecting", .reconnecting), ("cancelling", .cancelling), ("disconnecting", .disconnecting), ("disconnected", .disconnected), ("failed", .failed), ("unavailable", .unavailable)] {
        scenes.append(("connection-\(name)", AnyView(DesktopConnectionJourneyView(model: library, journey: .init(desktop: desktops[2], stage: stage)))))
    }

    for size in [CGSize(width: 680, height: 460), CGSize(width: 1000, height: 700)] {
        for dark in [false, true] {
            for (name, scene) in scenes {
                let appearance = try #require(NSAppearance(named: dark ? .darkAqua : .aqua))
                let root = scene
                    .environment(\.colorScheme, dark ? .dark : .light)
                    .environment(\.scenePhase, .inactive)
                    .frame(width: size.width, height: size.height)
                let host = NSHostingView(rootView: root)
                let window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: .borderless, backing: .buffered, defer: false)
                window.isReleasedWhenClosed = false
                window.appearance = appearance
                host.appearance = appearance
                window.contentView = host
                host.frame = NSRect(origin: .zero, size: size)
                host.layoutSubtreeIfNeeded()
                // Let SwiftUI/AppKit settle layout and field-focus work, offscreen.
                try await Task.sleep(for: .milliseconds(40))
                host.layoutSubtreeIfNeeded()
                let bitmap = try #require(host.bitmapImageRepForCachingDisplay(in: host.bounds))
                appearance.performAsCurrentDrawingAppearance {
                    host.cacheDisplay(in: host.bounds, to: bitmap)
                }
                #expect(bitmap.pixelsWide >= Int(size.width))
                #expect(bitmap.pixelsHigh >= Int(size.height))
                // Empty/error states must fill the viewport too, not leave a
                // transparent letterbox when their intrinsic height is shorter.
                for (x, y) in [(0, 0), (bitmap.pixelsWide - 1, bitmap.pixelsHigh - 1)] {
                    let corner = try #require(bitmap.colorAt(x: x, y: y))
                    #expect(corner.alphaComponent > 0.99)
                }
                let png = try #require(bitmap.representation(using: .png, properties: [:]))
                #expect(png.count > 1000)
                try png.write(to: output.appendingPathComponent("\(name)-\(dark ? "dark" : "light")-\(Int(size.width)).png"))
                window.contentView = nil
                window.close()
            }
        }
    }
}
