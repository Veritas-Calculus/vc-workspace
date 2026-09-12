import AppKit
import SwiftUI

struct WorkspaceSceneBackground: View {
    var body: some View { WorkspaceStyle.canvas.ignoresSafeArea() }
}

struct WorkspaceBrandLockup: View {
    var iconSize: CGFloat = 56
    var body: some View {
        HStack(spacing: 12) {
            Image(nsImage: NSApplication.shared.applicationIconImage)
                .resizable().interpolation(.high)
                .frame(width: iconSize, height: iconSize)
                .accessibilityHidden(true)
            Text("VC Workspace")
                .font(.system(size: 25, weight: .semibold)).tracking(-0.5)
        }
        .accessibilityElement(children: .combine)
    }
}

struct WorkspaceLoadingView: View {
    let stage: ClientLoadingStage
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ZStack {
            WorkspaceSceneBackground()
            VStack(spacing: 24) {
                Image(nsImage: NSApplication.shared.applicationIconImage)
                    .resizable().interpolation(.high)
                    .frame(width: 88, height: 88)
                    .accessibilityHidden(true)
                Text(stage.title)
                    .font(WorkspaceStyle.title).tracking(-0.4)
                    .multilineTextAlignment(.center)
                    .id(stage.title)
                    .transition(WorkspaceStyle.transition(reduced: reduceMotion))
                WorkspaceActivityStroke()
            }
            .padding(WorkspaceStyle.pageInset)
        }
        .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: stage)
        .accessibilityElement(children: .contain)
    }
}

struct DesktopConnectionJourneyView: View {
    @ObservedObject var model: AppModel
    let journey: DesktopConnectionJourney
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ZStack {
            WorkspaceSceneBackground()
            VStack(spacing: 0) {
                ConnectionStatusArtwork(stage: journey.stage)
                    .padding(.bottom, 28)
                Text(journey.desktop.name)
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
                    .help(journey.desktop.name)
                    .padding(.bottom, 9)
                Text(journey.stage.title)
                    .font(WorkspaceStyle.title).tracking(-0.5)
                    .id(journey.stage.title)
                    .transition(WorkspaceStyle.transition(reduced: reduceMotion))
                Text(journey.detail)
                    .font(.system(size: 13)).foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 380, minHeight: 38, alignment: .top)
                    .padding(.top, 10)
                connectionActions
                    .frame(height: 80, alignment: .top)
                    .padding(.top, 18)
            }
            .frame(maxWidth: 420)
            .padding(WorkspaceStyle.pageInset)
        }
        .animation(WorkspaceStyle.motion(reduced: reduceMotion), value: journey.stage)
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder private var connectionActions: some View {
        if journey.stage.canCancel {
            Button { model.cancelConnection() } label: {
                Text("取消").frame(width: 128)
            }
            .buttonStyle(WorkspaceActionStyle(emphasis: .secondary))
            .keyboardShortcut(.cancelAction)
        } else if journey.stage.canRetry {
            VStack(spacing: 12) {
                Button { model.retryConnection() } label: {
                    Label("重新连接", systemImage: "arrow.clockwise").frame(width: 160)
                }
                .buttonStyle(WorkspaceActionStyle())
                .keyboardShortcut(.defaultAction)
                Button("返回桌面库") { model.returnToDesktopLibrary() }
                    .buttonStyle(.borderless)
            }
        } else if journey.stage == .unavailable {
            Button("返回桌面库") { model.returnToDesktopLibrary() }
                .buttonStyle(WorkspaceActionStyle())
                .keyboardShortcut(.defaultAction)
        }
    }
}

struct ConnectionStatusArtwork: View {
    let stage: DesktopConnectionStage
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var symbol: String {
        switch stage {
        case .starting: "power"
        case .preparing: "display"
        case .opening: "arrow.up.forward"
        case .reconnecting: "arrow.clockwise"
        case .cancelling, .disconnecting: "rectangle.portrait.and.arrow.right"
        case .failed: "exclamationmark.triangle"
        case .unavailable: "lock"
        case .disconnected: "display"
        }
    }

    var body: some View {
        VStack(spacing: 22) {
            Image(systemName: symbol)
                .font(.system(size: 54, weight: .light))
                .foregroundStyle(stage == .failed ? Color(nsColor: .systemRed) : WorkspaceStyle.ink)
                .contentTransition(reduceMotion ? .identity : .symbolEffect(.replace))
                .frame(width: 132, height: 112)
                .background(WorkspaceStyle.surface, in: RoundedRectangle(cornerRadius: WorkspaceStyle.cardRadius, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: WorkspaceStyle.cardRadius, style: .continuous)
                        .strokeBorder(WorkspaceStyle.line.opacity(0.55), lineWidth: 1)
                }
            WorkspaceActivityStroke(pending: stage.isPending)
                .opacity(stage.isPending ? 1 : 0)
        }
        .frame(width: 164, height: 148)
        .accessibilityHidden(true)
    }
}

struct SessionDisplayTransitionView: View {
    var body: some View {
        ZStack {
            WorkspaceStyle.canvas
            VStack(spacing: 24) {
                ConnectionStatusArtwork(stage: .opening)
                Text("正在调整画面").font(WorkspaceStyle.title)
            }
        }
        .accessibilityElement(children: .combine)
    }
}
