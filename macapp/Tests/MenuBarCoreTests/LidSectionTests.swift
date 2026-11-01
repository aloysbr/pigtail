import AppKit
import SwiftUI
import XCTest
@testable import MenuBarCore

private func on(_ deadline: Date?, actionError: LidActionError? = nil, isActing: Bool = false) -> LidDisplay {
    .fold(response: LidResponse(sleepDisabled: true, deadline: deadline), readFailure: nil, unavailable: false,
          actionError: actionError, isActing: isActing)
}
private func off(actionError: LidActionError? = nil, isActing: Bool = false) -> LidDisplay {
    .fold(response: LidResponse(sleepDisabled: false), readFailure: nil, unavailable: false,
          actionError: actionError, isActing: isActing)
}

final class LidSectionTests: XCTestCase {
    func testHHMMIsLocalAndTwoDigits() {
        let paris = TimeZone(identifier: "Europe/Paris")!
        let d = ISO8601DateFormatter().date(from: "2026-10-16T22:05:00Z")!   // 00:05 à Paris (CEST)
        XCTAssertEqual(LidFormat.hhmm(d, timeZone: paris), "00:05")
        XCTAssertEqual(LidFormat.hhmm(d, timeZone: TimeZone(identifier: "UTC")!), "22:05")
    }

    func testD13LinesExact() {
        let paris = TimeZone(identifier: "Europe/Paris")!
        let d = ISO8601DateFormatter().date(from: "2026-10-16T21:00:00Z")!
        XCTAssertEqual(LidFormat.activeLine(on(d), timeZone: paris), "Travel Mode jusqu'à 23:00")
        XCTAssertEqual(LidFormat.activeLine(on(nil)), "Travel Mode actif (sans minuteur)")
        XCTAssertNil(LidFormat.activeLine(off())); XCTAssertNil(LidFormat.activeLine(.loading))
        XCTAssertEqual(LidSectionModel(display: off()).stateLine, "Travel Mode désactivé")
        XCTAssertEqual(LidSectionModel(display: on(d), timeZone: paris).stateLine, "Travel Mode jusqu'à 23:00")
        XCTAssertEqual(LidSectionModel(display: on(nil)).stateLine, "Travel Mode actif (sans minuteur)")
    }

    func testButtonsPerState() {      // D19
        let offM = LidSectionModel(display: off())
        XCTAssertTrue(offM.isVisible)
        XCTAssertEqual(offM.header, "Travel Mode")
        XCTAssertEqual(offM.durationLabel, "Activer")
        XCTAssertEqual(offM.durationButtons.map(\.title), ["1 h", "4 h", "8 h"])
        XCTAssertEqual(offM.durationButtons.map(\.action), [.activate(hours: 1), .activate(hours: 4), .activate(hours: 8)])
        XCTAssertNil(offM.deactivateButton)
        for d in [on(nil), on(Date())] {
            let m = LidSectionModel(display: d)
            XCTAssertEqual(m.header, "Travel Mode")
            XCTAssertEqual(m.durationLabel, "Prolonger")
            XCTAssertEqual(m.durationButtons.map(\.title), ["1 h", "4 h", "8 h"])
            XCTAssertEqual(m.durationButtons.map(\.action), [.extend(hours: 1), .extend(hours: 4), .extend(hours: 8)])
            XCTAssertEqual(m.deactivateButton?.title, "Désactiver"); XCTAssertEqual(m.deactivateButton?.action, .deactivate)
        }
        let unread: [LidDisplay] = [
            .fold(response: nil, readFailure: .unreachable, unavailable: false, actionError: nil, isActing: false),
            .fold(response: nil, readFailure: .failed("x"), unavailable: false, actionError: nil, isActing: false),
            .fold(response: nil, readFailure: nil, unavailable: true, actionError: nil, isActing: false),
        ]
        for d in unread {
            let m = LidSectionModel(display: d)
            XCTAssertTrue(m.isVisible, "\(d.state)")
            XCTAssertNil(m.durationLabel); XCTAssertEqual(m.durationButtons, []); XCTAssertNil(m.deactivateButton)
            XCTAssertNil(m.stateLine); XCTAssertNil(m.symbol)
        }
        XCTAssertFalse(LidSectionModel(display: .loading).isVisible)
    }

    func testSymbolsPerState() {
        XCTAssertEqual(LidSectionModel(display: on(nil)).symbol, LidGlyph.travel)
        XCTAssertEqual(LidSectionModel(display: on(Date())).symbol, LidGlyph.travel)
        XCTAssertEqual(LidSectionModel(display: off()).symbol, LidGlyph.off)
        XCTAssertNotNil(NSImage(systemSymbolName: LidGlyph.travel, accessibilityDescription: nil))
        XCTAssertNotNil(NSImage(systemSymbolName: LidGlyph.off, accessibilityDescription: nil))
    }

    func testNotSetUpTextExactNoButtonAdded() {
        for (plain, failing) in [(off(), off(actionError: .notSetUp)), (on(nil), on(nil, actionError: .notSetUp))] {
            let base = LidSectionModel(display: plain)
            let m = LidSectionModel(display: failing)
            XCTAssertEqual(m.notices.first, "lancer pigtail lid setup dans un terminal")
            XCTAssertEqual(LidFormat.notSetUp, "lancer pigtail lid setup dans un terminal")
            XCTAssertEqual(m.durationButtons, base.durationButtons)
            XCTAssertEqual(m.deactivateButton, base.deactivateButton)
        }
    }

    /// G15: Travel Mode unreadable while the daemon answers (the section is reached at all) says
    /// « Démon injoignable » — the app's own word for the daemon (« démon »), not « Daemon ».
    func testUnreadableTravelWithDaemonUpSaysDemonInjoignable() {
        let unreachable = LidSectionModel(display: .fold(response: nil, readFailure: .unreachable, unavailable: false,
                                                        actionError: nil, isActing: false))
        XCTAssertEqual(unreachable.notices, ["Démon injoignable"])
        XCTAssertFalse(unreachable.notices.contains("Daemon injoignable"))
    }

    func testErrorsAreVisible() {      // D19
        func m(_ r: LidResponse?, read: LidReadFailure? = nil, action: LidActionError? = nil) -> LidSectionModel {
            LidSectionModel(display: .fold(response: r, readFailure: read, unavailable: false, actionError: action, isActing: false))
        }
        let off = LidResponse(sleepDisabled: false)
        XCTAssertEqual(m(off, action: .failed("boom")).notices, ["Échec : boom"])
        XCTAssertEqual(m(off, action: .unreachable).notices, ["Démon injoignable"])
        XCTAssertEqual(m(off, action: .unavailable).notices, ["Travel Mode indisponible sur ce daemon"])
        XCTAssertEqual(m(LidResponse(sleepDisabled: false, lastError: "x")).notices, ["Échec : x"])
        // lecture ratée après une lecture réussie : même notice quelle que soit la nature
        XCTAssertEqual(m(off, read: .unreachable).notices, ["Lecture échouée — état précédent affiché"])
        XCTAssertEqual(m(off, read: .failed("pmset -g failed")).notices, ["Lecture échouée — état précédent affiché"])
        // jamais lu : la nature décide
        XCTAssertEqual(m(nil, read: .unreachable).notices, ["Démon injoignable"])
        XCTAssertEqual(m(nil, read: .failed("pmset -g failed")).notices, ["Échec : pmset -g failed"])
        XCTAssertFalse(m(nil, read: .failed("pmset -g failed")).notices.contains("Démon injoignable"))
        for model in [m(nil, read: .unreachable), m(off, action: .unreachable)] {
            XCTAssertFalse(model.notices.contains { $0.contains("Échec : daemon injoignable") })
        }
        XCTAssertEqual(LidSectionModel(display: .fold(response: nil, readFailure: nil, unavailable: true,
                                                       actionError: nil, isActing: false)).notices,
                       ["Travel Mode indisponible sur ce daemon"])
    }

    func testButtonsDisabledWhileActing() {
        XCTAssertFalse(LidSectionModel(display: off(isActing: true)).buttonsEnabled)
        XCTAssertFalse(LidSectionModel(display: on(nil, isActing: true)).buttonsEnabled)
        XCTAssertTrue(LidSectionModel(display: off()).buttonsEnabled)
    }

    /// The state line's text must start at the same x on and off: the slot is at least as wide as the
    /// widest of the two symbols (measured at the line's `.callout` font), so neither overflows it.
    @MainActor
    func testSymbolSlotFitsBothSymbols() {
        for name in [LidGlyph.travel, LidGlyph.off] {
            let host = NSHostingView(rootView: Image(systemName: name).font(.callout))
            XCTAssertLessThanOrEqual(host.fittingSize.width, LidSectionView.symbolWidth, name)
        }
        XCTAssertEqual(LidSectionView.symbolWidth,
                       max(NSHostingView(rootView: Image(systemName: LidGlyph.travel).font(.callout)).fittingSize.width,
                           NSHostingView(rootView: Image(systemName: LidGlyph.off).font(.callout)).fittingSize.width),
                       "the slot is the widest symbol, no wider")
    }
}
