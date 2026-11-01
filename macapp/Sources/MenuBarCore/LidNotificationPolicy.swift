import UserNotifications

/// Whether the Travel Mode notices can show (Q2), as one Bool: refused
/// (`.denied`), never answered (`.notDetermined`), provisional, or alerts
/// turned off in System Settings all give `true` — and the same orange line,
/// `LidFormat.notificationsDenied`.
public enum LidNotificationPolicy {
    public static func denied(status: UNAuthorizationStatus, alert: UNNotificationSetting) -> Bool {
        status != .authorized || alert == .disabled
    }
}
