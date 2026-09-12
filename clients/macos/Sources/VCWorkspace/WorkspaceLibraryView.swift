import SwiftUI

private struct WorkspaceSearchActionKey: FocusedValueKey {
    typealias Value = () -> Void
}

extension FocusedValues {
    var workspaceSearchAction: (() -> Void)? {
        get { self[WorkspaceSearchActionKey.self] }
        set { self[WorkspaceSearchActionKey.self] = newValue }
    }
}

struct DesktopListView: View {
    @ObservedObject var model: AppModel
    @Binding var searchText: String
    @FocusState private var searchFocused: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.scenePhase) private var scenePhase
    private let columns = [GridItem(.adaptive(minimum: 280, maximum: 360), spacing: WorkspaceStyle.gap, alignment: .top)]

    private var filteredDesktops: [Desktop] { DesktopLibrary.filtered(model.desktops, query: searchText) }

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .center, spacing: 16) {
                Text("桌面").font(WorkspaceStyle.title).tracking(-0.5)
                Spacer()
                HStack(spacing: 7) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    TextField("搜索桌面", text: $searchText)
                        .textFieldStyle(.plain).focused($searchFocused)
                        .help("搜索名称、VM 编号或节点（⌘F）")
                        .onExitCommand { searchText = "" }
                    if !searchText.isEmpty {
                        Button { searchText = ""; searchFocused = true } label: {
                            Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                        }
                        .buttonStyle(.plain).help("清除搜索").accessibilityLabel("清除搜索")
                    }
                }
                .padding(.horizontal, 10).frame(width: 220, height: 34)
                .background(WorkspaceStyle.surface, in: RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius))
                .overlay {
                    RoundedRectangle(cornerRadius: WorkspaceStyle.controlRadius)
                        .strokeBorder(searchFocused ? Color.accentColor : WorkspaceStyle.line.opacity(0.5), lineWidth: searchFocused ? 2 : 1)
                        .allowsHitTesting(false)
                }
                Button { Task { await model.loadDesktops() } } label: {
                    Image(systemName: "arrow.clockwise")
                        .symbolEffect(.pulse, options: .repeating, isActive: WorkspaceStyle.animatesActivity(
                            pending: model.isRefreshing, reduced: reduceMotion, active: scenePhase == .active
                        ))
                        .frame(width: 28, height: 28)
                }
                .buttonStyle(.borderless)
                .help("刷新桌面（⌘R）").accessibilityLabel("刷新桌面")
                .disabled(model.isRefreshing || model.connectingVMID != nil)
                Menu {
                    Button("退出登录") { Task { await model.logout() } }
                } label: {
                    Image(systemName: "person.crop.circle").font(.system(size: 20))
                }
                .menuStyle(.borderlessButton).menuIndicator(.hidden)
                .fixedSize().help("账户").accessibilityLabel("账户")
            }
            .padding(WorkspaceStyle.pageInset)

            if !model.errorMessage.isEmpty, !model.desktops.isEmpty {
                Label(model.errorMessage, systemImage: "exclamationmark.circle")
                    .font(.callout).foregroundStyle(Color(nsColor: .systemRed))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, WorkspaceStyle.pageInset).padding(.bottom, 16)
            }
            libraryContent
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background { WorkspaceSceneBackground() }
        .focusedSceneValue(\.workspaceSearchAction, { searchFocused = true })
    }

    @ViewBuilder private var libraryContent: some View {
        if model.desktops.isEmpty && model.isRefreshing {
            WorkspaceLoadingView(stage: .loadingDesktops)
        } else if model.desktops.isEmpty && !model.errorMessage.isEmpty {
            ContentUnavailableView {
                Label("无法加载桌面", systemImage: "exclamationmark.triangle")
            } description: { Text(model.errorMessage) } actions: {
                Button("重试") { Task { await model.loadDesktops() } }
                    .buttonStyle(WorkspaceActionStyle()).disabled(model.isRefreshing)
            }
        } else if model.desktops.isEmpty {
            ContentUnavailableView("没有可用桌面", systemImage: "display")
        } else if filteredDesktops.isEmpty {
            ContentUnavailableView {
                Label("没有匹配的桌面", systemImage: "magnifyingglass")
            } description: { Text("没有找到与“\(searchText)”匹配的桌面") } actions: {
                Button("清除搜索") { searchText = ""; searchFocused = true }
            }
        } else {
            ScrollView {
                LazyVGrid(columns: columns, alignment: .leading, spacing: WorkspaceStyle.gap) {
                    ForEach(filteredDesktops) { desktop in DesktopCard(model: model, desktop: desktop) }
                }
                .padding(.horizontal, WorkspaceStyle.pageInset)
                .padding(.bottom, WorkspaceStyle.pageInset)
                .frame(maxWidth: .infinity, alignment: .topLeading)
            }
        }
    }
}

struct DesktopCard: View {
    @ObservedObject var model: AppModel
    let desktop: Desktop
    @State private var hovering = false
    private var running: Bool { desktop.status == "running" }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(alignment: .center) {
                Image(systemName: "display")
                    .font(.system(size: 32, weight: .light))
                    .foregroundStyle(running ? WorkspaceStyle.ink : .secondary)
                    .accessibilityHidden(true)
                Spacer()
                Label(DesktopLibrary.statusTitle(desktop.status), systemImage: running ? "checkmark.circle" : "power")
                    .font(.system(size: 12)).foregroundStyle(.secondary)
            }
            VStack(alignment: .leading, spacing: 7) {
                Text(desktop.name).font(WorkspaceStyle.heading)
                    .lineLimit(1).truncationMode(.tail).help(desktop.name)
                Text("VM \(String(desktop.vmid)) · \(desktop.node)")
                    .font(.system(size: 12)).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
                    .help("VM \(desktop.vmid) · \(desktop.node)")
            }
            HStack(spacing: 14) {
                Label("\(desktop.cpuCount) 核", systemImage: "cpu")
                Label(ByteCountFormatter.string(fromByteCount: desktop.memoryTotal, countStyle: .memory), systemImage: "memorychip")
            }
            .font(.system(size: 12)).foregroundStyle(.secondary)
            Button { model.beginConnection(to: desktop) } label: {
                HStack {
                    Text(running ? "连接" : "启动并连接")
                    Spacer()
                    Image(systemName: "arrow.up.right")
                }
            }
            .buttonStyle(WorkspaceActionStyle())
            .accessibilityLabel("\(running ? "连接" : "启动并连接") \(desktop.name)")
            .disabled(model.connectingVMID != nil)
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(WorkspaceStyle.surface, in: RoundedRectangle(cornerRadius: WorkspaceStyle.cardRadius, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: WorkspaceStyle.cardRadius, style: .continuous)
                .strokeBorder(WorkspaceStyle.line.opacity(hovering ? 1 : 0.5), lineWidth: 1)
                .allowsHitTesting(false)
        }
        .onHover { hovering = $0 }
        .animation(.easeOut(duration: 0.15), value: hovering)
        .accessibilityElement(children: .contain)
    }
}
