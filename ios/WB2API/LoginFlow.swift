import SwiftUI
import SafariServices

/// 应用内浏览器（SFSafariViewController）包装：打开上游授权页完成 OAuth 登录。
struct SafariView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> SFSafariViewController {
        let vc = SFSafariViewController(url: url)
        vc.preferredControlTintColor = .systemBlue
        return vc
    }

    func updateUIViewController(_ uiViewController: SFSafariViewController, context: Context) {}
}

/// 添加账号：选版本（国内/国际）→ 打开授权页 → 自动轮询直到拿到凭证。
struct LoginSheet: View {
    @ObservedObject private var gw = WBGateway.shared
    @Environment(\.dismiss) private var dismiss

    @State private var realm = "cn"
    @State private var authURL: URL?
    @State private var status = ""
    @State private var starting = false
    @State private var finished = false
    @State private var timer: Timer?

    var body: some View {
        NavigationView {
            Group {
                if let u = authURL {
                    VStack(spacing: 0) {
                        SafariView(url: u)
                        bottomBar
                    }
                } else {
                    startPane
                }
            }
            .navigationBarTitle("添加账号", displayMode: .inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("关闭") { stop() ; gw.loginCancel(); dismiss() }
                }
            }
        }
        .navigationViewStyle(StackNavigationViewStyle())
        .onDisappear { stop() }
    }

    private var startPane: some View {
        VStack(spacing: 18) {
            Text("选择要登录的版本")
                .font(.headline)
            Picker("版本", selection: $realm) {
                Text("国内版").tag("cn")
                Text("国际版").tag("global")
            }
            .pickerStyle(SegmentedPickerStyle())
            .padding(.horizontal, 40)

            Text("点下方按钮后，在打开的页面里用你的 WorkBuddy / CodeBuddy 账号登录；登录完成后本页会自动识别并保存凭证。")
                .font(.footnote)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 24)

            Button(action: start) {
                HStack {
                    if starting { ProgressView().padding(.trailing, 4) }
                    Text(starting ? "正在获取授权地址…" : "开始授权登录")
                }
            }
            .disabled(starting)
            .padding(.top, 6)

            if !status.isEmpty {
                Text(status)
                    .font(.footnote)
                    .foregroundColor(.orange)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)
            }
            Spacer()
        }
        .padding(.top, 40)
    }

    private var bottomBar: some View {
        HStack(spacing: 12) {
            Text(status.isEmpty ? "在页面里完成登录，程序会自动识别" : status)
                .font(.footnote)
                .foregroundColor(.secondary)
            Spacer()
            Button("我已完成") { pollOnce() }
                .font(.footnote)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(Color(UIColor.secondarySystemBackground))
    }

    private func start() {
        starting = true
        status = ""
        DispatchQueue.global(qos: .userInitiated).async {
            let url = gw.loginStart(realm: realm)
            DispatchQueue.main.async {
                starting = false
                if let s = url, let u = URL(string: s) {
                    authURL = u
                    startPolling()
                } else if gw.alert == nil {
                    status = "获取授权地址失败，请重试"
                }
            }
        }
    }

    private func startPolling() {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { _ in
            pollOnce(silent: true)
        }
    }

    private func stop() {
        timer?.invalidate()
        timer = nil
    }

    private func pollOnce(silent: Bool = false) {
        guard !finished else { return }
        DispatchQueue.global(qos: .userInitiated).async {
            let (ok, err) = gw.loginPoll(realm: realm)
            DispatchQueue.main.async {
                if let msg = ok {
                    finished = true
                    stop()
                    gw.toast = msg
                    gw.refresh()
                    dismiss()
                    return
                }
                if !silent {
                    status = err ?? "尚未完成登录"
                }
            }
        }
    }
}
