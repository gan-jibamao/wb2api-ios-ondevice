import AVFoundation
import UIKit

/// 后台保活：借「音频」后台模式（UIBackgroundModes=audio），让 App 退到后台后不被系统挂起。
///
/// 原理：用 AVAudioSession(.playback) 持续播放一段**静音**音频。系统判定 App 正在播放音频，
/// 因此不会挂起进程 —— 本地 HTTP 服务（127.0.0.1:10082）得以在后台继续响应，
/// 同机其它 App（如 Minis）随时可以调用。
///
/// 三道保险（对齐社区最佳实践 StikDebug / mob_background）：
///   1. 2 秒健康自检：Apple 明确「长期无声会被系统触发 interruption 且不自动恢复」，故必须轮询重拉；
///   2. interruption / mediaServicesWereReset 通知：来电结束、媒体服务重置后重建引擎；
///   3. 强/省电两档：强档成为 Now Playing（系统优先保活，锁屏会显示"正在播放"），
///      省电档 .mixWithOthers（不出现在锁屏，但被内存回收的概率略高）。
///
/// 说明：这是侧载（自签/企业签）场景下的常规做法，App Store 审核会拒绝此类用途。完全无声。
final class KeepAlive {
    static let shared = KeepAlive()

    private var engine = AVAudioEngine()
    private var player = AVAudioPlayerNode()
    private var buffer: AVAudioPCMBuffer?
    private var attached = false
    private var scheduled = false
    private var observers: [NSObjectProtocol] = []
    private var healthTimer: Timer?

    /// 强力模式：不加 .mixWithOthers，成为 Now Playing（更抗内存回收，锁屏/控制中心会显示）
    private(set) var strong = false
    private(set) var running = false
    /// 健康自检累计重拉次数（便于界面观察稳定性）
    private(set) var recoverCount = 0

    private init() {}

    /// 开始保活（幂等）。strong=true 为强力模式。
    @discardableResult
    func start(strong: Bool = false) -> Bool {
        self.strong = strong
        return activate()
    }

    /// 停止保活
    func stop() {
        guard running || healthTimer != nil else { return }
        healthTimer?.invalidate()
        healthTimer = nil
        player.stop()
        engine.stop()
        try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
        running = false
        scheduled = false
        removeObservers()
    }

    private func activate() -> Bool {
        do {
            let session = AVAudioSession.sharedInstance()
            var opts: AVAudioSession.CategoryOptions = []
            if !strong { opts.insert(.mixWithOthers) }
            try session.setCategory(.playback, mode: .default, options: opts)
            try session.setActive(true)

            if !attached {
                engine.attach(player)
                guard let format = AVAudioFormat(standardFormatWithSampleRate: 8000, channels: 1) else {
                    return false
                }
                engine.connect(player, to: engine.mainMixerNode, format: format)
                let frames = AVAudioFrameCount(format.sampleRate) // 1 秒静音
                guard let buf = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames) else {
                    return false
                }
                buf.frameLength = frames // 未写入即全零 = 静音
                buffer = buf
                attached = true
            }

            if !engine.isRunning {
                try engine.start()
            }
            // buffer 只 schedule 一次（.loops 循环），重复 schedule 会叠加占用
            if !scheduled, let buf = buffer {
                player.scheduleBuffer(buf, at: nil, options: [.loops], completionHandler: nil)
                scheduled = true
            }
            if !player.isPlaying {
                player.play()
            }
            running = true
            registerObservers()
            startHealthCheck()
            return true
        } catch {
            running = false
            return false
        }
    }

    /// 健康自检：每 2 秒确认「引擎在跑 + 播放器在播 + 会话活跃」，任一失效立即重拉。
    private func startHealthCheck() {
        healthTimer?.invalidate()
        healthTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            guard let self else { return }
            let ok = self.engine.isRunning && self.player.isPlaying
            if !ok {
                self.recoverCount += 1
                _ = self.recover()
            }
        }
        RunLoop.main.add(healthTimer!, forMode: .common)
    }

    /// 轻量恢复（引擎对象仍有效时）
    @discardableResult
    private func recover() -> Bool {
        try? AVAudioSession.sharedInstance().setActive(true)
        if !engine.isRunning {
            try? engine.start()
        }
        if engine.isRunning && !player.isPlaying {
            player.play()
        }
        running = engine.isRunning
        return running
    }

    /// 媒体服务被系统重置：原 AVAudioEngine/AVAudioPlayerNode 已失效，必须重建
    private func rebuild() {
        player.stop()
        engine.stop()
        engine = AVAudioEngine()
        player = AVAudioPlayerNode()
        attached = false
        scheduled = false
        running = false
        _ = activate()
    }

    private func registerObservers() {
        guard observers.isEmpty else { return }
        let c = NotificationCenter.default

        // 来电/其它音频打断结束后，重新拉起静音播放
        observers.append(c.addObserver(forName: AVAudioSession.interruptionNotification,
                                       object: nil, queue: .main) { [weak self] note in
            guard let self else { return }
            guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                  let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
            if type == .ended {
                _ = self.recover()
            }
        })

        // 媒体服务重置 → 整套重建
        observers.append(c.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification,
                                       object: nil, queue: .main) { [weak self] _ in
            self?.rebuild()
        })

        // 进入后台时确认一次
        observers.append(c.addObserver(forName: UIApplication.didEnterBackgroundNotification,
                                       object: nil, queue: .main) { [weak self] _ in
            guard let self else { return }
            if !self.engine.isRunning || !self.player.isPlaying {
                self.recoverCount += 1
                _ = self.recover()
            }
        })
    }

    private func removeObservers() {
        observers.forEach { NotificationCenter.default.removeObserver($0) }
        observers.removeAll()
    }
}
