import XCTest
@testable import MenuBarCore

final class SettingsModelTests: XCTestCase {
    private let utc = UsageFixtures.utc
    private let now = UsageFixtures.now
    private let nb = "\u{00A0}"

    private func lid(_ r: LidResponse?, read: LidReadFailure? = nil) -> LidDisplay {
        .fold(response: r, readFailure: read, unavailable: false, actionError: nil, isActing: false)
    }
    private func model(_ d: LidDisplay, daemonDown: Bool = false, denied: Bool = false,
                       loginOn: Bool = false, approval: Bool = false) -> SettingsModel {
        SettingsModel(lid: d, daemonDown: daemonDown, lidNotificationsDenied: denied, loginEnabled: loginOn,
                      loginNeedsApproval: approval, now: now, timeZone: utc)
    }

    func testTravelOnActivatesFourHours() {
        let m = model(lid(LidResponse(sleepDisabled: false)))
        XCTAssertFalse(m.travelOn)
        XCTAssertEqual(m.travelAction(turningOn: true), .activate(hours: 4), "D11")
        XCTAssertEqual(m.travelAction(turningOn: false), .deactivate)
        XCTAssertEqual(m.travelSubtitle, "Désactivé")
        XCTAssertTrue(m.extendButtons.isEmpty)
    }

    func testTravelActiveSubtitleAndExtend() {
        let m = model(lid(LidResponse(sleepDisabled: true, deadline: now.addingTimeInterval(139 * 60))))
        XCTAssertTrue(m.travelOn)
        XCTAssertEqual(m.travelSubtitle, "2\(nb)h\(nb)19 restantes · jusqu\u{2019}à 23:19")
        XCTAssertEqual(m.extendLabel, "Prolonger")
        XCTAssertEqual(m.extendButtons.map(\.title), ["1 h", "4 h", "8 h"])
        XCTAssertEqual(m.extendButtons.map(\.action), [.extend(hours: 1), .extend(hours: 4), .extend(hours: 8)])
    }

    func testTravelActiveWithoutTimer() {   // G8
        let m = model(lid(LidResponse(sleepDisabled: true)))
        XCTAssertEqual(m.travelSubtitle, "Actif, sans minuteur")
    }

    func testNotificationsDeniedNoticeKept() {   // #263
        let off = lid(LidResponse(sleepDisabled: false))
        XCTAssertTrue(model(off, denied: true).notices.contains(LidFormat.notificationsDenied))
        XCTAssertFalse(model(off, denied: false).notices.contains(LidFormat.notificationsDenied))
        let guarded = lid(LidResponse(sleepDisabled: true, guardError: "ioreg illisible"))
        XCTAssertTrue(model(guarded).notices.contains(LidFormat.guardDown("ioreg illisible")))
    }

    func testDaemonUnavailableGraysTravel() {
        let m = model(lid(nil, read: .unreachable), daemonDown: true, loginOn: true)
        XCTAssertFalse(m.travelEnabled)
        XCTAssertEqual(m.travelSubtitle, "Indisponible sans le démon")
        XCTAssertFalse(m.notices.contains(LidFormat.unreachable), "no « Démon injoignable » when the daemon is down altogether")
        XCTAssertTrue(m.loginRowEnabled, "« Démarrer à la connexion » stays usable")
        // daemon up, Travel Mode unreadable: the legend shows (G15)
        let up = model(lid(nil, read: .unreachable), daemonDown: false)
        XCTAssertTrue(up.notices.contains("Démon injoignable"))
        XCTAssertFalse(up.travelEnabled)
    }

    func testLoginRowAndApprovalCaption() {
        XCTAssertEqual(model(lid(LidResponse(sleepDisabled: false))).loginTitle, "Démarrer à la connexion")
        XCTAssertNil(model(lid(LidResponse(sleepDisabled: false))).loginCaption)
        XCTAssertNotNil(model(lid(LidResponse(sleepDisabled: false)), approval: true).loginCaption)
    }
}
