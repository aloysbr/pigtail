import AppCore
import Foundation
import MenuBarCore
import UserNotifications

/// Posts the Travel Mode notices as macOS notifications (D3) and says whether
/// they can show at all (Q2). Pigtail is a signed bundle (`sh.pigtail.app`),
/// which UserNotifications needs; pigtaild posts nothing. Async API only
/// (Swift 6): no completion handler crosses actors.
@MainActor
final class LidNotifier: NSObject, ObservableObject, UNUserNotificationCenterDelegate {
    /// Notifications cannot show (refused, never answered, alerts off, or no
    /// bundle): the Travel Mode section shows its orange line. Recomputed at
    /// every refresh, so it goes back to false when the user allows them again.
    @Published private(set) var denied = false
    /// nil outside a bundle: `refresh` and `post` then do nothing.
    private var center: UNUserNotificationCenter?
    private var refreshTask: Task<Void, Never>?

    func start() {
        // The release bundle only: a verify build or the bare executable never asks
        // for the authorisation (UNUserNotificationCenter.current() raises outside a bundle).
        guard AppIdentity.isReleaseBundle(Bundle.main.bundleIdentifier) else {
            BarLog.lid.notice("[Pigtail] notifications off: not the release bundle")
            denied = true
            return
        }
        let center = UNUserNotificationCenter.current()
        self.center = center
        center.delegate = self
        Task { [weak self] in
            _ = try? await center.requestAuthorization(options: [.alert])   // no sound
            await self?.refresh()
        }
        startRefreshing()
    }

    func refresh() async {
        guard let center else { return }
        let st = await center.notificationSettings()
        denied = LidNotificationPolicy.denied(status: st.authorizationStatus, alert: st.alertSetting)
    }

    /// Every `LidMonitor.defaultPollInterval` (the Travel Mode's own cadence),
    /// first read after one interval: no race with `start`'s authorization.
    private func startRefreshing() {
        guard refreshTask == nil else { return }
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(LidMonitor.defaultPollInterval))
                guard let self else { return }
                await self.refresh()
            }
        }
    }

    func post(_ n: LidCutNotice) {
        post(id: n.id, title: n.title, body: n.body)
    }

    /// Any one-line notice of the bar (Travel Mode cut, cmux check notice).
    func post(id: String, title: String, body: String) {
        guard let center else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        let req = UNNotificationRequest(identifier: id, content: content, trigger: nil)
        Task { [weak self] in
            do {
                try await center.add(req)
            } catch {
                BarLog.lid.error("[Pigtail] Travel Mode notification failed: \(String(describing: error), privacy: .public)")
                await self?.refresh()
            }
        }
    }

    /// Withdraws a notice by id (the low-disk one when the space is back):
    /// the delivered one and a pending one. Nothing outside the release bundle
    /// (`center` is nil there).
    func remove(id: String) {
        guard let center else { return }
        center.removeDeliveredNotifications(withIdentifiers: [id])
        center.removePendingNotificationRequests(withIdentifiers: [id])
    }

    /// Without it macOS shows no banner while Pigtail is the active app
    /// (popover open).
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        [.banner, .list]
    }
}
