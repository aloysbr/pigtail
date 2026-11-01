import AppKit
import XCTest
@testable import MenuBarCore

/// No test shows a window or activates the app: the panel's show/hide are injected and recorded.
@MainActor
final class MaintenanceChipPanelTests: XCTestCase {
    private var dir: URL!
    private var shown = 0
    private var hidden = 0

    override func setUpWithError() throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("maint-panel-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        shown = 0; hidden = 0
    }
    override func tearDownWithError() throws { try? FileManager.default.removeItem(at: dir) }

    private var windowURL: URL { dir.appendingPathComponent("window.json") }
    private let visible = NSRect(x: 0, y: 100, width: 1440, height: 800)   // 100 = the Dock's height

    private func make(runner: @escaping MaintenanceMonitor.TakeBackRunner = { _, _, _ in .init(status: 0, output: "") })
        -> (MaintenanceMonitor, MaintenanceChipPanel) {
        let m = MaintenanceMonitor(windowURL: windowURL, takeBackCommand: .init(executable: URL(fileURLWithPath: "/fake/take-back"), arguments: ["--back", "--by", "chip"]),
                                   now: { Date(timeIntervalSince1970: 1792663260) }, runner: runner)
        let p = MaintenanceChipPanel(monitor: m, visibleFrame: { [visible] in visible },
                                     present: { [unowned self] _ in self.shown += 1 },
                                     dismiss: { [unowned self] _ in self.hidden += 1 })
        return (m, p)
    }

    func testPanelNeverBecomesKeyOrMain() {
        let (_, p) = make()
        XCTAssertFalse(p.canBecomeKey)
        XCTAssertFalse(p.canBecomeMain)
        XCTAssertEqual(p.level, .statusBar)
    }

    func testPanelIsNonActivating() {
        let (_, p) = make()
        XCTAssertTrue(p.styleMask.contains(.nonactivatingPanel))
        XCTAssertTrue(p.styleMask.contains(.borderless))
        XCTAssertFalse(p.hidesOnDeactivate)
        XCTAssertEqual(p.collectionBehavior, [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary])
        XCTAssertFalse(p.becomesKeyOnlyIfNeeded && p.canBecomeKey)
    }

    func testPanelHiddenWhenDisplayHidden() throws {
        let (m, p) = make()
        XCTAssertEqual(p.shownDisplay, .hidden)
        XCTAssertEqual(shown, 0)
        try MaintenanceFixtures.data("running").write(to: windowURL)
        m.poll()
        XCTAssertEqual(shown, 1)
        guard case .running = p.shownDisplay else { return XCTFail("running expected") }
        try FileManager.default.removeItem(at: windowURL)
        m.poll()
        XCTAssertEqual(p.shownDisplay, .hidden)
        XCTAssertGreaterThanOrEqual(hidden, 1)
    }

    func testPanelBottomCenterAboveDock() {
        let f = MaintenanceChipPanel.frame(size: NSSize(width: 640, height: 84), in: visible)
        XCTAssertEqual(f.midX, visible.midX, accuracy: 0.5)
        XCTAssertEqual(f.minY, visible.minY + 16, accuracy: 0.001)
        XCTAssertEqual(f.size, NSSize(width: 640, height: 84))
        XCTAssertGreaterThanOrEqual(f.minY, 100, "never under the Dock")
    }

    func testPanelBackdropIsDarkHudEffectView() throws {
        let (_, p) = make()
        let fx = try XCTUnwrap(p.contentView as? NSVisualEffectView)
        XCTAssertEqual(fx.material, .hudWindow)
        XCTAssertEqual(fx.state, .active)
        XCTAssertEqual(fx.blendingMode, .behindWindow)
        XCTAssertEqual(fx.appearance?.name, .darkAqua)
        XCTAssertEqual(fx.layer?.cornerRadius, 22)
    }

    func testPanelWidthNeverExceedsTheScreen() throws {
        XCTAssertEqual(MaintenanceChipPanel.chipWidth(in: visible), 760)
        XCTAssertEqual(MaintenanceChipPanel.chipWidth(in: NSRect(x: 0, y: 0, width: 600, height: 400)), 568)
    }

    func testPanelIsPlacedAtBottomCenterOnceShown() throws {
        let (m, p) = make()
        try MaintenanceFixtures.data("running").write(to: windowURL)
        m.poll()
        XCTAssertEqual(p.frame.midX, visible.midX, accuracy: 1)
        XCTAssertEqual(p.frame.minY, visible.minY + 16, accuracy: 0.5)
        XCTAssertGreaterThanOrEqual(p.frame.width, 600)
    }

    func testReprendreCallsTakeBack() async throws {
        final class Count: @unchecked Sendable { var n = 0 }
        let c = Count()
        let (m, p) = make(runner: { _, args, _ in c.n += 1; XCTAssertEqual(args, ["--back", "--by", "chip"]); return .init(status: 0, output: "") })
        try MaintenanceFixtures.data("running").write(to: windowURL)
        m.poll()
        await p.takeBackTapped().value
        XCTAssertEqual(c.n, 1)
    }

    func testReprendreFailureShownInChip() async throws {
        let (m, p) = make(runner: { _, _, _ in .init(status: 1, output: "AutoRaise non rétabli\n") })
        try MaintenanceFixtures.data("running").write(to: windowURL)
        m.poll()
        await p.takeBackTapped().value
        XCTAssertEqual(p.shownDisplay, .failed("AutoRaise non rétabli", warnings: []))
        XCTAssertEqual(p.shownDisplay.lines, ["Maintenance — AutoRaise non rétabli"])
    }
}
