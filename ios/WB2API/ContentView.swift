import SwiftUI

struct ContentView: View {
    @ObservedObject private var gw = WBGateway.shared
    @State private var showLogin = false
    @State private var showImport = false
    @AppStorage("autoStart") private var autoStart = true
    @AppStorage("keepAlive") private var keepAlive = true
    @AppStorage("keepAliveStrong") private var keepAliveStrong = false

    private var alertBinding: Binding<Bool> {
        Binding(get: { gw.alert != nil }, set: { if !$0 { gw.alert = nil } })
    }

    var body: some View {
        NavigationView {
            List {
                statusSection
                controlSection
                accountSection
                keySection
                tipSection
            }
            .listStyle(InsetGroupedListStyle())
            .navigationBarTitle("WB 反代", displayMode: .large)
            .sheet(isPresented: $showLogin) { LoginSheet() }
            .sheet(isPresented: $showImport) { ImportSheet() }
            .alert("提示", isPresented: alertBinding) {
                Button("好", role: .cancel) { gw.alert = nil }
            } message: {
                Text(gw.alert ?? "")
            }
            .onAppear {
                gw.refresh()
                if autoStart && !gw.running { gw.start() }
            }
        }
        .navigationViewStyle(StackNavigationViewStyle())
    }

    // MARK: - 分区

    private var statusSection: some View {
        Section {
            HStack(spacing: 14) {
                ZStack {
                    Circle()
                        .fill(gw.running ? Color.green.opacity(0.16) : Color.gray.opacity(0.16))
                        .frame(width: 44, height: 44)
                    Circle()
                        .fill(gw.running ? Color.green : Color.gray)
                        .frame(width: 13, height: 13)
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text(gw.running ? "反代运行中" : "反代已停止")
                        .font(.headline)
                    if let st = gw.status, gw.running {
                        Text("http://\(st.addr)")
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Text("账号 \(st.accounts.count) 个 · 可用 \(st.available) 个 · 运行 \(st.uptimeText)")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    } else {
                        Text("点下方按钮启动")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    if gw.running {
                        Text(gw.keepAliveOn ? "后台保活已开启" : "后台保活未开启")
                            .font(.caption2)
                            .foregroundColor(gw.keepAliveOn ? .green : .orange)
                    }
                }
                Spacer()
                if gw.busy { ProgressView() }
            }
            .padding(.vertical, 4)
        }
    }

    private var controlSection: some View {
        Section {
            Button(action: { if gw.running { gw.stop() } else { gw.start() } }) {
                HStack {
                    Image(systemName: gw.running ? "stop.circle.fill" : "play.circle.fill")
                    Text(gw.running ? "停止反代" : "启动反代")
                }
            }
            .disabled(gw.busy)

            Toggle("打开 App 时自动启动", isOn: $autoStart)

            Toggle("后台保活（静音音频）", isOn: $keepAlive)
                .onChange(of: keepAlive) { on in gw.applyKeepAlive(on) }

            if keepAlive {
                Toggle("保活强力模式（锁屏显示为播放中）", isOn: $keepAliveStrong)
                    .onChange(of: keepAliveStrong) { _ in gw.applyKeepAlive(keepAlive) }
            }
        }
    }

    private var accountSection: some View {
        Section(header: Text("账号池")) {
            if let st = gw.status, !st.accounts.isEmpty {
                ForEach(st.accounts) { a in
                    VStack(alignment: .leading, spacing: 3) {
                        HStack {
                            Text(a.nickname.isEmpty ? String(a.uid.prefix(8)) : a.nickname)
                                .font(.subheadline)
                            Text(a.realmText)
                                .font(.caption2)
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .background(a.realm == "global" ? Color.blue.opacity(0.15) : Color.orange.opacity(0.15))
                                .cornerRadius(4)
                            Spacer()
                            Text(a.expiryText)
                                .font(.caption2)
                                .foregroundColor(.secondary)
                        }
                        Text(a.domain)
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                    .swipeActions {
                        Button(role: .destructive) {
                            gw.deleteAccount(a.uid)
                        } label: {
                            Label("删除", systemImage: "trash")
                        }
                    }
                }
            } else {
                Text(gw.running ? "还没有账号，点下面「添加账号」登录" : "先启动反代")
                    .font(.footnote)
                    .foregroundColor(.secondary)
            }

            Button(action: { showLogin = true }) {
                HStack {
                    Image(systemName: "person.badge.plus")
                    Text("添加账号（OAuth 登录）")
                }
            }

            Button(action: { showImport = true }) {
                HStack {
                    Image(systemName: "square.and.arrow.down.on.square")
                    Text("导入 JSON 账号（粘贴 / 文件）")
                }
            }
        }
    }

    private var keySection: some View {
        Section(header: Text("API Key"), footer: Text("客户端调用地址：http://127.0.0.1:7863/v1 ，Authorization 填这个 Key。同机其它 App（含 Minis）可直接访问。")) {
            if let st = gw.status, !st.apiKey.isEmpty {
                HStack {
                    Text(st.apiKey)
                        .font(.system(.footnote, design: .monospaced))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer()
                    Button(action: {
                        UIPasteboard.general.string = st.apiKey
                        gw.toast = "API Key 已复制"
                    }) {
                        Image(systemName: "doc.on.doc")
                    }
                    .buttonStyle(BorderlessButtonStyle())
                }
            } else {
                Text("启动后显示")
                    .font(.footnote)
                    .foregroundColor(.secondary)
            }
        }
    }

    private var tipSection: some View {
        Section(header: Text("使用提示")) {
            Text("· 定时任务：签到 / 猫猫旅行 / 活跃上报 / token 保活（默认开启）")
            Text("· 数据目录：文件 App → 我的 iPhone → WB反代 → WB2API")
            Text("· iOS 会在 App 进入后台后挂起服务，已内置静音音频保活；如仍被回收请重新打开 App")
            Text("· 开学季、夜猫子任务依赖外部脚本，此版本已关闭")
        }
        .font(.footnote)
    }
}
