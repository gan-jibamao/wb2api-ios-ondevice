import SwiftUI
import UniformTypeIdentifiers

/// 导入账号 JSON：粘贴文本 / 选文件 / 剪贴板一键贴。
/// 支持嵌套{account,auth}、扁平、数组、{"accounts":[…]}、手机号映射形。
struct ImportSheet: View {
    @ObservedObject private var gw = WBGateway.shared
    @Environment(\.dismiss) private var dismiss

    @State private var text = ""
    @State private var result: WBImportResult?
    @State private var working = false
    @State private var showPicker = false
    @State private var localError: String?

    var body: some View {
        NavigationView {
            VStack(spacing: 0) {
                editor
                Divider()
                if let r = result {
                    resultPane(r)
                    Divider()
                }
                if let e = localError {
                    Text(e)
                        .font(.footnote)
                        .foregroundColor(.red)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal)
                        .padding(.top, 6)
                }
                bottomBar
            }
            .navigationBarTitle("导入 JSON 账号", displayMode: .inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("关闭") { dismiss() }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("粘贴") {
                        if let s = UIPasteboard.general.string { text = s }
                    }
                    .disabled(working)
                }
            }
            .fileImporter(isPresented: $showPicker,
                          allowedContentTypes: [.json, .plainText, .text],
                          allowsMultipleSelection: false) { res in
                load(res)
            }
        }
        .navigationViewStyle(StackNavigationViewStyle())
    }

    // MARK: - 输入区

    private var editor: some View {
        ZStack(alignment: .topLeading) {
            TextEditor(text: $text)
                .font(.system(.footnote, design: .monospaced))
                .padding(6)
                .disabled(working)

            if text.isEmpty {
                Text("""
                粘贴账号 JSON，支持：
                · 嵌套 {account, auth}
                · 扁平 {uid, accessToken, …}
                · 数组 [ {...}, {...} ]
                · 信封 {"accounts": [...]}
                · 映射 {"手机号": {...}}
                一次可导入多个账号
                """)
                .font(.footnote)
                .foregroundColor(.secondary)
                .padding(.horizontal, 12)
                .padding(.vertical, 14)
                .allowsHitTesting(false)
            }
        }
        .frame(minHeight: 190)
    }

    // MARK: - 结果区

    private func resultPane(_ r: WBImportResult) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 6) {
                Text("共 \(r.total) 条 · 新增 \(r.added) · 更新 \(r.updated) · 跳过 \(r.skipped)")
                    .font(.subheadline).bold()

                ForEach(r.items) { it in
                    HStack(alignment: .top, spacing: 6) {
                        Text(it.statusText)
                            .font(.caption2)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(badgeColor(it.status).opacity(0.16))
                            .cornerRadius(4)

                        VStack(alignment: .leading, spacing: 2) {
                            Text(it.nickname.isEmpty ? it.uid : "\(it.nickname) · \(it.uid.prefix(8))")
                                .font(.caption)
                            if let why = it.reason, !why.isEmpty {
                                Text(why)
                                    .font(.caption2)
                                    .foregroundColor(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        Spacer(minLength: 0)
                    }
                }
            }
            .padding(.horizontal)
            .padding(.vertical, 8)
        }
        .frame(maxHeight: 210)
    }

    private var bottomBar: some View {
        HStack(spacing: 12) {
            Button(action: { showPicker = true }) {
                Label("选文件", systemImage: "folder")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(BorderedButtonStyle())
            .disabled(working)

            Button(action: run) {
                HStack(spacing: 6) {
                    if working { ProgressView().scaleEffect(0.8) }
                    Text(working ? "导入中…" : "开始导入")
                }
                .frame(maxWidth: .infinity)
            }
            .buttonStyle(BorderedProminentButtonStyle())
            .disabled(working || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .padding(.horizontal)
        .padding(.vertical, 12)
    }

    // MARK: - 行为

    private func badgeColor(_ s: String) -> Color {
        switch s {
        case "added": return .green
        case "updated": return .blue
        default: return .orange
        }
    }

    private func run() {
        localError = nil
        result = nil
        working = true
        let payload = text

        DispatchQueue.global(qos: .userInitiated).async {
            let r = gw.importJSON(payload)
            DispatchQueue.main.async {
                working = false
                if let r = r {
                    result = r
                    if r.skipped == r.total {
                        localError = "全部被跳过，看下面每条的跳过原因"
                    } else {
                        gw.toast = "新增 \(r.added) · 更新 \(r.updated) · 跳过 \(r.skipped)"
                    }
                } else {
                    localError = gw.alert ?? "导入失败"
                }
            }
        }
    }

    private func load(_ res: Result<[URL], Error>) {
        switch res {
        case .failure(let e):
            localError = e.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                let data = try Data(contentsOf: url)
                if let s = String(data: data, encoding: .utf8) {
                    text = s
                    localError = nil
                } else {
                    localError = "文件不是 UTF-8 文本"
                }
            } catch {
                localError = "读取失败：\(error.localizedDescription)"
            }
        }
    }
}
