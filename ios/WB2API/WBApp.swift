import SwiftUI

@main
struct WBApp: App {
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            ContentView()
        }
        .onChange(of: scenePhase) { phase in
            let gw = WBGateway.shared
            let d = UserDefaults.standard
            let wanted = (d.object(forKey: "keepAlive") as? Bool) ?? true
            let strong = (d.object(forKey: "keepAliveStrong") as? Bool) ?? false

            if phase == .background {
                if gw.running && wanted {
                    _ = KeepAlive.shared.start(strong: strong)
                }
            } else if phase == .active {
                gw.refresh()
                if gw.running {
                    if wanted && !KeepAlive.shared.running {
                        _ = KeepAlive.shared.start(strong: strong)
                    }
                } else {
                    let auto = (d.object(forKey: "autoStart") as? Bool) ?? true
                    if auto { gw.start() }
                }
            }
        }
    }
}
