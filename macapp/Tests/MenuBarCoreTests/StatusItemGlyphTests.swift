import AppKit
import XCTest
@testable import MenuBarCore

/// The status item's glyph is data exported from the mockup's glyph.py
/// (variant D of t23). These tests pin the data's shape and hold the code's
/// drawing to the render the designer validated.
final class StatusItemGlyphTests: XCTestCase {

    // MARK: - The data

    /// Skull (two eyes + the snout's ring), snout disc (two nostrils), two ears.
    func testTheGlyphIsFourPiecesWithFiveHoles() {
        let holes = StatusItemGlyph.polygons.map { $0.count - 1 }.sorted()
        XCTAssertEqual(holes, [0, 0, 2, 3])
    }

    func testEveryRingIsAnOpenPolygonOfCoordinatePairs() {
        for polygon in StatusItemGlyph.polygons {
            for ring in polygon {
                XCTAssertEqual(ring.count % 2, 0, "x, y pairs")
                XCTAssertGreaterThanOrEqual(ring.count, 8, "at least four points")
                let n = ring.count
                XCTAssertFalse(ring[0] == ring[n - 2] && ring[1] == ring[n - 1], "a ring is open")
            }
        }
    }

    /// The simplified points stay in the exported ink box and still touch it
    /// on every side, to the simplification's own tolerance (0.002 u).
    func testThePointsFillTheExportedInkBox() {
        let box = StatusItemGlyph.inkBounds
        var seen = CGRect.null
        for polygon in StatusItemGlyph.polygons {
            for ring in polygon {
                for i in stride(from: 0, to: ring.count, by: 2) {
                    let p = CGPoint(x: ring[i], y: ring[i + 1])
                    XCTAssertTrue(box.insetBy(dx: -1e-5, dy: -1e-5).contains(p), "\(p) outside the ink box")
                    seen = seen.union(CGRect(origin: p, size: .zero))
                }
            }
        }
        XCTAssertEqual(seen.minX, box.minX, accuracy: 0.002)
        XCTAssertEqual(seen.maxX, box.maxX, accuracy: 0.002)
        XCTAssertEqual(seen.minY, box.minY, accuracy: 0.002)
        XCTAssertEqual(seen.maxY, box.maxY, accuracy: 0.002)
    }

    // MARK: - The drawing against the validated render

    static func fixtureURL() -> URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("Fixtures/status-item-glyph-D-2x.png")
    }

    /// Alpha of every pixel of `rep` (RGBA, 8 bits), row by row.
    static func alphas(_ rep: NSBitmapImageRep) -> [Int] {
        guard let data = rep.bitmapData else { return [] }
        var out: [Int] = []
        for y in 0..<rep.pixelsHigh {
            for x in 0..<rep.pixelsWide {
                out.append(Int(data[y * rep.bytesPerRow + x * rep.samplesPerPixel + 3]))
            }
        }
        return out
    }

    /// The code's glyph at 2× (36 px) against glyph-D-2x.png, rendered by
    /// Inkscape from the unsimplified geometry. Measured on the prototype:
    /// IoU 0.991, mean |Δα| 0.0017, max |Δα| 7/255. The thresholds leave
    /// room for antialiasing differences, not for a misplaced or flipped glyph.
    func testTheDrawingMatchesTheValidatedRender() throws {
        let reference = try XCTUnwrap(NSImage(contentsOf: Self.fixtureURL()))
        let code = Self.alphas(StatusItemImageTests.raster(StatusItemImage.base(), scale: 2))
        let ref = Self.alphas(StatusItemImageTests.raster(reference, scale: 2))
        XCTAssertEqual(code.count, 36 * 36)
        XCTAssertEqual(ref.count, 36 * 36)
        var inter = 0, union = 0, sum = 0, worst = 0
        for (a, b) in zip(code, ref) {
            if a >= 128 && b >= 128 { inter += 1 }
            if a >= 128 || b >= 128 { union += 1 }
            sum += abs(a - b)
            worst = max(worst, abs(a - b))
        }
        let iou = Double(inter) / Double(max(union, 1))
        let mean = Double(sum) / Double(code.count) / 255
        XCTAssertGreaterThanOrEqual(iou, 0.97, "IoU \(iou)")
        XCTAssertLessThanOrEqual(mean, 0.02, "mean |Δα| \(mean)")
        XCTAssertLessThanOrEqual(worst, 64, "max |Δα| \(worst)")
    }

    /// Verify's capture: writes the code's glyph at 1× and 2× into
    /// GLYPH_CAPTURE_DIR, to be compared by eye with glyph-D-2x.png.
    func testCaptureTheGlyph() throws {
        guard let dir = ProcessInfo.processInfo.environment["GLYPH_CAPTURE_DIR"] else {
            throw XCTSkip("GLYPH_CAPTURE_DIR not set")
        }
        for scale in [1, 2] {
            let rep = StatusItemImageTests.raster(StatusItemImage.base(), scale: scale)
            let png = try XCTUnwrap(rep.representation(using: .png, properties: [:]))
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("status-item-glyph-\(scale)x.png"))
        }
    }
}
