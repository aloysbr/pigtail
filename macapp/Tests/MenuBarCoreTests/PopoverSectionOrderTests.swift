import XCTest

/// D2 (refonte): the popover is the usage view. `PopoverView.swift` (app target, which no test
/// compiles) is the card around `PopoverContent`; read its source. Red on the old full popover.
final class PopoverSectionOrderTests: XCTestCase {
    private func code() throws -> String {
        let source = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()   // macapp/
            .appendingPathComponent("Sources/Pigtail/Views/PopoverView.swift")
        let text = try String(contentsOf: source, encoding: .utf8)
            .components(separatedBy: "\n")
            .filter { !$0.trimmingCharacters(in: .whitespaces).hasPrefix("//") }
            .joined(separator: "\n")
        XCTAssertGreaterThan(text.count, 500, "instrument control: the source was not read")
        return text
    }

    func testPopoverIsTheUsageContentInsideTheCard() throws {
        let c = try code()
        XCTAssertTrue(c.contains("PopoverContent("), "the card wraps PopoverContent")
        XCTAssertTrue(c.contains("reportsPanelMeasure"), "the card still reports its size to the panel")
        XCTAssertTrue(c.contains("lidNotificationsDenied:"), "the denied-notifications notice stays wired (#263)")
    }

    func testOldSectionsAreGone() throws {
        let c = try code()
        for gone in ["GatesSectionView", "ServersSectionModel", "StartDisclosure", "PurgeMenu", "battery.100",
                     "Stop all", "Démarrer au login", "inaccessible", "deathRows", "sidebarBanner",
                     "showsBackupSection"] {
            XCTAssertFalse(c.contains(gone), "\(gone) left the popover (D2)")
        }
    }
}
