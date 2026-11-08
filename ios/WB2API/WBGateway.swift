import Foundation
import Combine

// MARK: - 数据模型（与 Go 侧 StatusJSON 对齐）

struct WBAccount: Identifiable, Decodable {
    var id: String { uid }
    let uid: String
    let nickname: String
    let realm: String
    let domain: String
    let expiresAt: Int64
    let file: String

    enum CodingKeys: String, CodingKey {
        case uid, nickname, realm, domain, file
        case expiresAt = "expires_at"
    }

    /// 凭证剩余有效期文案
    var expiryText: String {
        guard expiresAt > 0 else { return "—" }
        let left = expiresAt - Int64(Date().timeIntervalSince1970)
        if left <= 0 { return "已过期" }
        let d = left / 86400
        if d > 0 { return "剩 \(d) 天" }
        return "剩 \(left / 3600) 小时"
    }

    var realmText: String { realm == "global" ? "国际" : "国内" }
}

struct WBStatus: Decodable {
    let running: Bool
    let addr: String
    let uptime: Int
    let dataDir: String
    let apiKey: String
    let redisMode: String
    let accounts: [WBAccount]
    let available: Int
    let version: String

    enum CodingKeys: String, CodingKey {
        case running, addr, uptime, accounts, available, version
        case dataDir = "data_dir"
        case apiKey = "api_key"
        case redisMode = "redis_mode"
    }

    var baseURL: String { "http://\(addr)" }
    var uptimeText: String {
        if uptime < 60 { return "\(uptime) 秒" }
        if uptime < 3600 { return "\(uptime / 60) 分钟" }
        return "\(uptime / 3600) 小时 \((uptime % 3600) / 60) 分"
    }
}

/// 登录接口的统一信封（Go 侧返回 {"ok":…,"data":…,"error":…}）
struct WBEnvelope: Decodable {
    let ok: Bool
    let error: String?
}

/// 带 data 载荷的信封（导入接口用；登录仍走 WBEnvelope，不改既有路径）
struct WBDataEnvelope<T: Decodable>: Decodable {
    let ok: Bool
    let error: String?
    let data: T?
}

/// 导入结果条目（对应 Go 侧 wbios.ImportItem）
struct WBImportItem: Decodable, Identifiable {
    var id: String { uid + "|" + status + "|" + (reason ?? "") }
    let uid: String
    let nickname: String
    let status: String   // added / updated / skipped
    let reason: String?
    let file: String?

    var statusText: String {
        switch status {
        case "added": return "新增"
        case "updated": return "更新"
        default: return "跳过"
        }
    }
}

/// 导入汇总（对应 Go 侧 wbios.ImportResult）
struct WBImportResult: Decodable {
    let total: Int
    let added: Int
    let updated: Int
    let skipped: Int
    let items: [WBImportItem]
}

// MARK: - 网关控制器

final class WBGateway: ObservableObject {
    static let shared = WBGateway()

    @Published private(set) var status: WBStatus?
    @Published private(set) var running = false
    @Published private(set) var busy = false
    @Published private(set) var keepAliveOn = false
    @Published var alert: String?
    @Published var toast: String?

    /// 数据目录：Documents/WB2API（文件 App 可见）
    let dataDir: URL

    private var timer: Timer?

    private init() {
        let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        dataDir = docs.appendingPathComponent("WB2API", isDirectory: true)
        try? FileManager.default.createDirectory(at: dataDir, withIntermediateDirectories: true)
        refresh()
        // 每 3 秒刷新一次状态（运行中时）
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            guard let self, self.running else { return }
            self.refresh()
        }
    }

    // MARK: Go 调用辅助

    private func wbString(_ f: () -> UnsafeMutablePointer<CChar>?) -> String {
        guard let p = f() else { return "" }
        defer { WbFree(p) }
        return String(cString: p)
    }

    private func wbCall(_ dataDir: String, _ f: (UnsafeMutablePointer<CChar>) -> UnsafeMutablePointer<CChar>?) -> String {
        dataDir.withCString { c in
            wbString { f(UnsafeMutablePointer(mutating: c)) }
        }
    }

    private func wbCall2(_ dataDir: String, _ arg: String,
                         _ f: (UnsafeMutablePointer<CChar>, UnsafeMutablePointer<CChar>) -> UnsafeMutablePointer<CChar>?) -> String {
        dataDir.withCString { c in
            arg.withCString { a in
                wbString { f(UnsafeMutablePointer(mutating: c), UnsafeMutablePointer(mutating: a)) }
            }
        }
    }

    // MARK: 状态

    func refresh() {
        let json = wbString { WbStatus() }
        if let d = json.data(using: .utf8), let st = try? JSONDecoder().decode(WBStatus.self, from: d) {
            status = st
            running = st.running
        } else {
            running = WbRunning() == 1
        }
    }

    // MARK: 启停

    func start() {
        guard !busy, !running else { return }
        busy = true
        let dir = dataDir.path
        let listen = ""
        let key = ""
        DispatchQueue.global(qos: .userInitiated).async {
            let ret = dir.withCString { cDir in
                listen.withCString { cListen in
                    key.withCString { cKey in
                        WbStart(UnsafeMutablePointer(mutating: cDir),
                                UnsafeMutablePointer(mutating: cListen),
                                UnsafeMutablePointer(mutating: cKey))
                    }
                }
            }
            var err: String?
            if let p = ret { err = String(cString: p); WbFree(p) }
            DispatchQueue.main.async {
                self.busy = false
                if let e = err, !e.isEmpty { self.alert = e }
                self.refresh()
                if self.running {
                    self.applyKeepAlive(UserDefaults.standard.object(forKey: "keepAlive") as? Bool ?? true)
                }
            }
        }
    }

    func stop() {
        guard !busy, running else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            let ret = WbStop()
            var err: String?
            if let p = ret { err = String(cString: p); WbFree(p) }
            DispatchQueue.main.async {
                self.busy = false
                if let e = err, !e.isEmpty { self.alert = e }
                KeepAlive.shared.stop()
                self.keepAliveOn = false
                self.refresh()
            }
        }
    }

    // MARK: 保活

    func applyKeepAlive(_ wanted: Bool) {
        if wanted && running {
            let strong = (UserDefaults.standard.object(forKey: "keepAliveStrong") as? Bool) ?? false
            keepAliveOn = KeepAlive.shared.start(strong: strong)
        } else {
            KeepAlive.shared.stop()
            keepAliveOn = false
        }
    }

    // MARK: 账号

    func deleteAccount(_ uid: String) {
        let ret = wbCall2(dataDir.path, uid) { cDir, cUid in WbDeleteAccount(cDir, cUid) }
        if let d = ret.data(using: .utf8), let env = try? JSONDecoder().decode(WBEnvelope.self, from: d), env.ok {
            toast = "已删除账号"
        } else if let d = ret.data(using: .utf8), let env = try? JSONDecoder().decode(WBEnvelope.self, from: d) {
            alert = env.error
        }
        refresh()
    }

    /// 发起登录，返回授权 URL（失败返回 nil 并设置 alert）
    func loginStart(realm: String) -> String? {
        let ret = wbCall2(dataDir.path, realm) { cDir, cRealm in WbLoginStart(cDir, cRealm) }
        return decodeEnvelopeString(ret)
    }

    /// 轮询一次；成功返回账号描述（并刷新列表）
    func loginPoll(realm: String) -> (String?, String?) {
        let ret = wbCall2(dataDir.path, realm) { cDir, cRealm in WbLoginPoll(cDir, cRealm) }
        guard let d = ret.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else {
            return (nil, "返回数据异常")
        }
        if let ok = obj["ok"] as? Bool, ok {
            if let dataStr = obj["data"] as? String,
               let dd = dataStr.data(using: .utf8),
               let m = try? JSONSerialization.jsonObject(with: dd) as? [String: Any] {
                let nick = (m["nickname"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? (m["uid"] as? String ?? "")
                refresh()
                return ("已添加账号：\(nick)", nil)
            }
            refresh()
            return ("已添加账号", nil)
        }
        return (nil, obj["error"] as? String ?? "登录未完成")
    }

    func loginCancel() {
        _ = wbCall(dataDir.path) { cDir in
            WbLoginCancel(cDir)
            return nil
        }
    }

    /// 导入账号 JSON（粘贴 / 文件）。失败返回 nil，原因写进 alert。
    /// 同步调用 Go 内核：纯本地解析 + 落盘，无网络，几十条也是毫秒级；
    /// 调用方仍应在后台线程调用，避免大文本卡主线程。
    @discardableResult
    func importJSON(_ raw: String) -> WBImportResult? {
        let ret = wbCall2(dataDir.path, raw) { cDir, cJSON in WbImportJSON(cDir, cJSON) }
        guard let d = ret.data(using: .utf8) else {
            DispatchQueue.main.async { self.alert = "返回数据异常" }
            return nil
        }
        let dec = JSONDecoder()
        if let env = try? dec.decode(WBDataEnvelope<WBImportResult>.self, from: d) {
            if env.ok, let r = env.data {
                DispatchQueue.main.async { self.refresh() }
                return r
            }
            DispatchQueue.main.async { self.alert = env.error ?? "导入失败" }
            return nil
        }
        // 兜底：只拿到 {"ok":false,"error":…}
        if let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any],
           let e = obj["error"] as? String {
            DispatchQueue.main.async { self.alert = e }
        } else {
            DispatchQueue.main.async { self.alert = "返回数据异常" }
        }
        return nil
    }

    private func decodeEnvelopeString(_ ret: String) -> String? {
        guard let d = ret.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else { return nil }
        if let ok = obj["ok"] as? Bool, ok, let s = obj["data"] as? String { return s }
        if let e = obj["error"] as? String {
            DispatchQueue.main.async { self.alert = e }
        }
        return nil
    }
}
