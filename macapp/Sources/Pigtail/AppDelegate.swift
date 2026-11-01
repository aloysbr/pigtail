import AppCore
import AppKit
import MenuBarCore
import os
import WindowCore

/// The one application delegate: composes the menu bar (`MenuBarController`)
/// and the main window (`MainWindowCoordinator`). Glue only; each decision
/// goes through a tested value of AppCore. Every log line a proof reads
/// interpolates its values `privacy: .public` (BarLog's convention).
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    static let log = Logger(subsystem: BarLog.subsystem, category: "app")

    private let coordinator = MainWindowCoordinator()
    private let loginItem = LoginItemController()
    private lazy var menuBar = MenuBarController(coordinator: coordinator, loginItem: loginItem)

    /// Watches relaunches of a retired app (release bundle only). Kept here:
    /// a released `NSKeyValueObservation` stops observing.
    private var retiredObservation: NSKeyValueObservation?
    /// Pids the relaunch watcher already judged.
    private var seenPids = RetiredPidTracker()

    func applicationWillFinishLaunching(_ notification: Notification) {
        // A reopen at login must not relaunch Pigtail as a user launch (window open).
        NSApp.disableRelaunchOnLogin()
        let menu = MainMenu.build(reloadTarget: coordinator, reloadAction: #selector(MainWindowCoordinator.reload(_:)))
        NSApp.mainMenu = menu
        NSApp.windowsMenu = menu.items.first { $0.title == MainMenu.windowTitle }?.submenu
        // No Apple event read here: the `oapp` is not current yet.
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // a. Launch context: the `oapp` event is current only here.
        let event = NSAppleEventManager.shared().currentAppleEvent
        let propData = event?.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue
        let launchedAsLoginItem = event?.eventID == kAEOpenApplication && propData == keyAELaunchedAsLogInItem
        let context = LaunchContext.decide(launchedAsLoginItem: launchedAsLoginItem, arguments: CommandLine.arguments)
        let eventID = event.map { Self.fourCC($0.eventID) } ?? "none"
        let prop = propData.map(Self.fourCC) ?? "none"
        Self.log.notice("[Pigtail] launch context: \(context.rawValue, privacy: .public) (eventID=\(eventID, privacy: .public) propData=\(prop, privacy: .public))")

        // b. Retired apps, release bundle only: the watcher first, then the sweep.
        retireOldApps()

        // c. Legacy preferences, before the status item (its position depends on them).
        let alreadyDone = UserDefaults.standard.bool(forKey: LegacyDefaultsMigration.doneKey)
        let copied = LegacyDefaultsMigration.run(
            into: .standard,
            window: UserDefaults(suiteName: LegacyDefaultsMigration.legacyWindowDomain)!,
            bar: UserDefaults(suiteName: LegacyDefaultsMigration.legacyBarDomain)!)
        if !copied.isEmpty {
            Self.log.notice("[Pigtail] legacy defaults migrated: \(copied.joined(separator: ", "), privacy: .public)")
        } else if alreadyDone {
            Self.log.notice("[Pigtail] legacy defaults: already done")
        } else {
            Self.log.notice("[Pigtail] legacy defaults: nothing to carry")
        }

        // d. The menu bar (no login-item bootstrap of its own).
        menuBar.start()

        // e. The only login-item bootstrap.
        loginItem.bootstrapIfNeeded(bundleInApplications: Bundle.main.bundleURL.path.hasPrefix("/Applications/"))

        // f. The window, for a user launch only.
        if context.opensWindow { coordinator.show() }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        AppLifecycle.terminatesAfterLastWindowClosed
    }

    /// Dock click, or `open -a` on the running app with the window closed.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        coordinator.show()
        return false // reopening is handled here
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        coordinator.appDidBecomeActive()
    }

    // MARK: Retired apps

    private func retireOldApps() {
        let own = Bundle.main.bundleIdentifier
        guard RetiredAppsPolicy.isActive(ownBundleID: own) else {
            Self.log.notice("[Pigtail] retired apps left alone: not the release bundle")
            return
        }
        let retired = LegacyDefaultsMigration.retiredBundleIDs
        // KVO, not didLaunchApplicationNotification: that one is not posted
        // for an LSUIElement app, and the old menu bar app is one.
        retiredObservation = NSWorkspace.shared.observe(\.runningApplications, options: [.new]) { @Sendable [weak self] _, change in
            // Plain values across the hop: NSRunningApplication is not Sendable.
            let added = (change.newValue ?? []).map { (pid: $0.processIdentifier, id: $0.bundleIdentifier) }
            Task { @MainActor in self?.judgeRelaunched(added, own: own, retired: retired) }
        }
        for id in retired {
            for app in NSRunningApplication.runningApplications(withBundleIdentifier: id)
            where RetiredAppsPolicy.shouldTerminate(runningBundleID: app.bundleIdentifier, ownBundleID: own, retired: retired) {
                seenPids.markJudged(app.processIdentifier)
                let sent = app.terminate()
                Self.log.notice("[Pigtail] retired app terminate: \(id, privacy: .public) pid \(app.processIdentifier, privacy: .public) sent=\(sent, privacy: .public)")
            }
        }
    }

    private func judgeRelaunched(_ added: [(pid: pid_t, id: String?)], own: String?, retired: [String]) {
        for pid in seenPids.judge(running: added, retired: retired) {
            let id = added.first { $0.pid == pid }?.id ?? ""
            let terminate = RetiredAppsPolicy.shouldTerminate(runningBundleID: id, ownBundleID: own, retired: retired)
            Self.log.notice("[Pigtail] retired app relaunched: \(id, privacy: .public) pid \(pid, privacy: .public) terminate=\(terminate, privacy: .public)")
            if terminate { NSRunningApplication(processIdentifier: pid)?.terminate() }
        }
    }

    /// A four-character code as text (`oapp`, `lgit`), for the launch log.
    nonisolated static func fourCC(_ code: UInt32) -> String {
        let bytes = [24, 16, 8, 0].map { UInt8((code >> $0) & 0xFF) }
        return String(bytes: bytes, encoding: .macOSRoman) ?? String(code)
    }
}
