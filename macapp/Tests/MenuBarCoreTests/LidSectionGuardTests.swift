import UserNotifications
import XCTest
@testable import MenuBarCore

/// Q2: the two orange lines of the Travel Mode section — a guard that cannot
/// read, and notifications that cannot show.
final class LidSectionGuardTests: XCTestCase {
    private func on(guardError: String?) -> LidDisplay {
        .fold(response: LidResponse(sleepDisabled: true, guardError: guardError), readFailure: nil,
              unavailable: false, actionError: nil, isActing: false)
    }

    func testGuardDownNotice() {
        let m = LidSectionModel(display: on(guardError: "thermal reading failed: x"))
        XCTAssertTrue(m.notices.contains("Garde inopérante : thermal reading failed: x"))
        XCTAssertEqual(LidFormat.guardDown("lid reading failed: y"), "Garde inopérante : lid reading failed: y")
    }

    func testNotificationsDeniedNotice() {
        let m = LidSectionModel(display: on(guardError: nil), notificationsDenied: true)
        XCTAssertTrue(m.notices.contains(LidFormat.notificationsDenied))
        XCTAssertEqual(LidFormat.notificationsDenied,
                       "Notifications non autorisées — les coupures du Travel Mode ne s'afficheront pas (Réglages Système › Notifications)")
    }

    func testDefaultShowsNeither() {
        let m = LidSectionModel(display: on(guardError: nil))
        XCTAssertFalse(m.notices.contains(LidFormat.notificationsDenied))
        XCTAssertFalse(m.notices.contains { $0.hasPrefix("Garde inopérante") })
        XCTAssertFalse(LidSectionModel(display: on(guardError: "")).notices.contains { $0.hasPrefix("Garde inopérante") })
    }

    /// Plan § « Écarts de preuve renvoyés au verify » 1: one Bool covers
    /// refused, never answered and alerts turned off — each gives the orange line.
    func testDeniedCoversTheThreeCases() {
        let cases: [(UNAuthorizationStatus, UNNotificationSetting, String)] = [
            (.denied, .enabled, "refused"),
            (.notDetermined, .notSupported, "never answered"),
            (.authorized, .disabled, "alerts off in System Settings"),
        ]
        for (status, alert, name) in cases {
            let denied = LidNotificationPolicy.denied(status: status, alert: alert)
            XCTAssertTrue(denied, name)
            let m = LidSectionModel(display: on(guardError: nil), notificationsDenied: denied)
            XCTAssertTrue(m.notices.contains(LidFormat.notificationsDenied), name)
        }
        XCTAssertFalse(LidNotificationPolicy.denied(status: .authorized, alert: .enabled), "allowed again → the line goes")
    }
}
