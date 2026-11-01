import XCTest
@testable import MenuBarCore

/// D9: the status item carries no badge for now. The label and the tooltip speak for
/// Travel Mode only (G11).
final class StatusItemSignalTests: XCTestCase {
    private let utc = TimeZone(identifier: "UTC")!

    private func lid(on: Bool, deadline: Date? = nil) -> LidDisplay {
        .fold(response: LidResponse(sleepDisabled: on, deadline: deadline),
              readFailure: nil, unavailable: false, actionError: nil, isActing: false)
    }

    func testLabelWithoutTravel() {
        XCTAssertEqual(StatusItemSignal.accessibilityLabel(lid: lid(on: false), timeZone: utc), "pigtail")
        XCTAssertEqual(StatusItemSignal.accessibilityLabel(lid: .loading, timeZone: utc), "pigtail")
    }

    func testLabelWithTravel() {
        // 2026-10-25 14:00:00 UTC
        let deadline = Date(timeIntervalSince1970: 1_793_023_200)
        XCTAssertEqual(StatusItemSignal.accessibilityLabel(lid: lid(on: true, deadline: deadline), timeZone: utc),
                       "pigtail, Travel Mode jusqu'à 14:00")
        XCTAssertEqual(StatusItemSignal.accessibilityLabel(lid: lid(on: true), timeZone: utc),
                       "pigtail, Travel Mode actif (sans minuteur)")
    }

    func testTooltipNilWhenOff() {
        XCTAssertNil(StatusItemSignal.tooltip(lid: lid(on: false), timeZone: utc))
        XCTAssertNil(StatusItemSignal.tooltip(lid: .loading, timeZone: utc))
        XCTAssertNil(StatusItemSignal.tooltip(
            lid: .fold(response: nil, readFailure: .unreachable, unavailable: false, actionError: nil, isActing: false),
            timeZone: utc))
    }

    func testTooltipIsTheTravelLine() {
        XCTAssertEqual(StatusItemSignal.tooltip(lid: lid(on: true), timeZone: utc), "Travel Mode actif (sans minuteur)")
    }

    /// The source names no badge, no gates and no backup: nothing but Travel Mode can light the glyph.
    func testNoBadgeAPI() throws {
        var url = URL(fileURLWithPath: #filePath)
        for _ in 0..<3 { url.deleteLastPathComponent() }
        let source = try String(contentsOf: url.appendingPathComponent("Sources/MenuBarCore/StatusItemSignal.swift"),
                                encoding: .utf8)
        XCTAssertGreaterThan(source.count, 100, "control: the source was read")
        for gone in ["Badge", "gates", "backup"] {
            XCTAssertFalse(source.contains(gone), "StatusItemSignal.swift must not mention \(gone)")
        }
    }
}
