import AppKit
import SwiftUI
import XCTest
@testable import MenuBarCore

/// Verify captures of the chip (no window shown, no focus taken): one bitmap per state from the real
/// fixtures. PNGs are written only when MAINT_CAPTURE_DIR is set.
@MainActor
final class MaintenanceChipCaptureTests: XCTestCase {
    private func display(_ name: String, now: Double) throws -> MaintenanceChipDisplay {
        MaintenanceChipDisplay.fold(window: try MaintenanceFixtures.window(name), readError: false, takeBackError: nil,
                                    now: Date(timeIntervalSince1970: now))
    }

    private func render(_ d: MaintenanceChipDisplay, taking: Bool = false, hover: Bool = false, width: CGFloat = 760) throws -> NSBitmapImageRep {
        let view = ZStack {
            LinearGradient(colors: [Color(red: 0.42, green: 0.56, blue: 0.85), Color(red: 0.23, green: 0.29, blue: 0.48)],
                           startPoint: .topLeading, endPoint: .bottomTrailing)
            MaintenanceChipView(display: d, isTakingBack: taking, width: width, previewHover: hover, onTakeBack: {}).padding(28)
        }
        let host = NSHostingView(rootView: view)
        host.appearance = NSAppearance(named: .darkAqua)
        let size = host.fittingSize
        host.frame = NSRect(origin: .zero, size: size)
        host.layoutSubtreeIfNeeded()
        let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        return rep
    }

    private func write(_ rep: NSBitmapImageRep, _ name: String) throws {
        guard let dir = ProcessInfo.processInfo.environment["MAINT_CAPTURE_DIR"] else { return }
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let png = try XCTUnwrap(rep.representation(using: .png, properties: [:]))
        try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("chip-\(name).png"))
    }

    /// The pixel at the dot (left of line 1) is the dot's colour, so a capture that is blank or has the
    /// wrong state colour fails.
    private func dotColour(_ rep: NSBitmapImageRep) throws -> NSColor {
        let s = CGFloat(rep.pixelsWide) / rep.size.width
        let c = try XCTUnwrap(rep.colorAt(x: Int((28 + 18 + 5) * s), y: Int((28 + 9 + 16) * s)))
        return try XCTUnwrap(c.usingColorSpace(.deviceRGB))
    }

    func testCaptureRunning() throws {
        let rep = try render(try display("running", now: 1792664100))   // now = updated: the bar is part-filled
        XCTAssertGreaterThan(rep.pixelsWide, 640)
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.greenComponent, 0.7)
        XCTAssertLessThan(c.redComponent, 0.4)
        try write(rep, "running")
    }

    func testCapturePassage() throws {
        let rep = try render(try display("passage", now: 1792665645))
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.redComponent, 0.8)
        XCTAssertGreaterThan(c.greenComponent, 0.7)
        XCTAssertLessThan(c.blueComponent, 0.3)
        try write(rep, "passage")
    }

    func testCaptureDone() throws {
        let rep = try render(try display("done", now: 1792667800))
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.blueComponent, 0.8)
        XCTAssertLessThan(c.redComponent, 0.5)
        try write(rep, "done")
    }

    func testCaptureFailed() throws {
        let rep = try render(try display("failed", now: 1792664500))
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.redComponent, 0.8)
        XCTAssertLessThan(c.blueComponent, 0.4)
        try write(rep, "failed")
    }

    func testCaptureOverrunIsAmber() throws {
        let now = 1792665780.0 + 190
        let w = try MaintenanceFixtures.window("running", replacing: [("\"updated\": 1792664100", "\"updated\": \(Int(now))")])
        let d = MaintenanceChipDisplay.fold(window: w, readError: false, takeBackError: nil, now: Date(timeIntervalSince1970: now))
        XCTAssertEqual(d.lines[0], "Maintenance · Re-index the project · dépassé +3 min")
        let rep = try render(d)
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.redComponent, 0.8)
        XCTAssertGreaterThan(c.greenComponent, 0.7)
        XCTAssertLessThan(c.blueComponent, 0.3)
        try write(rep, "overrun")
    }

    /// Fill of the « Reprendre » capsule, sampled in its left padding (no glyph there).
    private func buttonFill(_ rep: NSBitmapImageRep, width: CGFloat = 760) throws -> NSColor {
        let s = CGFloat(rep.pixelsWide) / rep.size.width
        let c = try XCTUnwrap(rep.colorAt(x: Int((28 + width - 10 - 92) * s), y: Int((28 + 9 + 16) * s)))
        return try XCTUnwrap(c.usingColorSpace(.deviceRGB))
    }

    func testCaptureHoverDimsTheTakeBackButton() throws {
        let d = try display("running", now: 1792664100)
        let rest = try buttonFill(try render(d)), hover = try buttonFill(try render(d, hover: true))
        XCTAssertGreaterThan(rest.redComponent, 0.97, "at rest the button is white")
        // Contrast hover vs rest >= 1.1:1 (WCAG ratio on the measured pixels).
        func lum(_ v: CGFloat) -> Double { let x = Double(v); return x <= 0.03928 ? x / 12.92 : pow((x + 0.055) / 1.055, 2.4) }
        let ratio = (lum(rest.redComponent) + 0.05) / (lum(hover.redComponent) + 0.05)
        XCTAssertGreaterThanOrEqual(ratio, 1.1, "hover must be visibly different from rest, measured \(ratio)")
        XCTAssertGreaterThanOrEqual(MaintenanceChipView.hoverButtonContrast, 1.1)
        // While it runs, the pointer changes nothing: « Reprise… » keeps its own fill.
        let takingHover = try buttonFill(try render(d, taking: true, hover: true))
        let taking = try buttonFill(try render(d, taking: true))
        XCTAssertEqual(takingHover.redComponent, taking.redComponent, accuracy: 0.001)
        try write(try render(d, hover: true), "hover")
    }

    func testCaptureTakingBackShowsDisabledLabel() throws {
        let d = try display("running", now: 1792664100)
        let idle = try render(d)
        let taking = try render(d, taking: true)
        XCTAssertNotEqual(idle.tiffRepresentation, taking.tiffRepresentation, "the button must look different while it runs")
        try write(taking, "taking")
    }

    func testCaptureTakeBackErrorIsRed() throws {
        // The chip shows what the monitor makes of handover's real exit-3 output, not an invented string.
        let cause = MaintenanceMonitor.takeBackFailureLine(status: 3, output: MaintenanceMonitorTests.realExit3Output)
        XCTAssertEqual(cause, "Fenêtre fermée, non rétabli : AutoRaise")
        let d = MaintenanceChipDisplay.fold(window: try MaintenanceFixtures.window("running"), readError: false,
                                            takeBackError: cause, now: Date(timeIntervalSince1970: 1792664100))
        XCTAssertEqual(d.lines[0], "Maintenance — Fenêtre fermée, non rétabli : AutoRaise")
        let rep = try render(d)
        let c = try dotColour(rep)
        XCTAssertGreaterThan(c.redComponent, 0.8)
        XCTAssertLessThan(c.blueComponent, 0.4)
        try write(rep, "takeback-error")
    }

    func testCaptureFailedWithWarnings() throws {
        let w = try MaintenanceFixtures.window("failed", replacing: [("\"warnings\": []", "\"warnings\": [\"Prio indisponible : ordre manuel\"]")])
        let d = MaintenanceChipDisplay.fold(window: w, readError: false, takeBackError: nil, now: Date(timeIntervalSince1970: 1792664500))
        XCTAssertEqual(d.lines.count, 3)
        let rep = try render(d)
        XCTAssertGreaterThan(rep.pixelsHigh, try render(try display("failed", now: 1792664500)).pixelsHigh + 10)
        try write(rep, "failed-warnings")
    }

    func testTakingBackButtonIsReadable() {
        // One dimming only (no `.disabled` on top): dark text on a light grey fill, WCAG AA.
        let r = MaintenanceChipView.takingBackButtonContrast
        XCTAssertGreaterThanOrEqual(r, 4.5, "« Reprise… » contrast \(r):1")
    }

    func testDotHaloIsEightPointsAroundTheDot() throws {
        let rep = try render(try display("running", now: 1792664100))
        let s = CGFloat(rep.pixelsWide) / rep.size.width
        let cx = (28 + 18 + 5) * s, cy = (28 + 9 + 16) * s
        func green(_ dx: CGFloat) throws -> CGFloat {
            let c = try XCTUnwrap(rep.colorAt(x: Int(cx + dx * s), y: Int(cy)))
            return try XCTUnwrap(c.usingColorSpace(.deviceRGB)).greenComponent
        }
        // 8 pt from the centre: inside the 18 pt halo (r = 9), outside the 4 pt-wide centred stroke (r <= 7).
        XCTAssertGreaterThan(try green(8) - (try green(14)), 0.03, "the halo must reach r = 9")
    }

    func testLongQueueStaysOnOneLine() throws {
        let items = (1...6).map { "{\"key\":\"k\($0)\",\"label\":\"Une session au libellé vraiment très long numéro \($0)\",\"need_min\":15}" }.joined(separator: ",")
        let json = """
        {"version":1,"state":"running","started":1792663200,"updated":1792663260,"until":null,
         "serving":{"key":"s","label":"Re-index the project","since":1792663260,"until":1792665780,"need_min":42},
         "queue":[\(items)],"served":[],"total_min":132,"passage":null,"ended":null,"ended_by":null,
         "warnings":[],"failure":null,"prior":{"lid":"off","lid_until":null,"autoraise":true}}
        """
        let w = try MaintenanceWindow.decode(Data(json.utf8))
        let d = MaintenanceChipDisplay.fold(window: w, readError: false, takeBackError: nil, now: Date(timeIntervalSince1970: 1792663260))
        let long = try render(d)
        let plain = try render(try display("running", now: 1792663260))
        XCTAssertEqual(long.pixelsHigh, plain.pixelsHigh, "a 6-session queue must stay on one line (D4-B nowrap)")
        XCTAssertEqual(long.pixelsWide, plain.pixelsWide)
        try write(long, "long-queue")
    }

    func testChipWidthIsStableAndCapped() throws {
        func width(_ d: MaintenanceChipDisplay, max: CGFloat = 760) -> CGFloat {
            NSHostingView(rootView: MaintenanceChipView(display: d, isTakingBack: false, width: max, onTakeBack: {})).fittingSize.width
        }
        let running = width(try display("running", now: 1792663260))
        let passage = width(try display("passage", now: 1792665645))
        XCTAssertEqual(running, passage, accuracy: 0.5, "« Reprendre » must not move at a passage")
        XCTAssertEqual(running, 760, accuracy: 0.5)
        let long = String(repeating: "très long libellé ", count: 12)
        let w = try MaintenanceFixtures.window("passage", replacing: [("\"label\": \"Scheduled backups\"", "\"label\": \"\(long)\"")])
        let d = MaintenanceChipDisplay.fold(window: w, readError: false, takeBackError: nil, now: Date(timeIntervalSince1970: 1792665645))
        XCTAssertLessThanOrEqual(width(d), 760.5)
        XCTAssertLessThanOrEqual(width(d, max: 500), 500.5, "a narrow screen caps the chip too")
    }

    func testCaptureWarningsAddAThirdLine() throws {
        let plain = try render(try display("running", now: 1792663260))
        let w = try MaintenanceFixtures.window("running", replacing: [("\"warnings\": []", "\"warnings\": [\"pigtail injoignable : ordre manuel\"]")])
        let warned = try render(MaintenanceChipDisplay.fold(window: w, readError: false, takeBackError: nil, now: Date(timeIntervalSince1970: 1792663260)))
        XCTAssertGreaterThan(warned.pixelsHigh, plain.pixelsHigh + 10)
        try write(warned, "running-warnings")
    }
}
