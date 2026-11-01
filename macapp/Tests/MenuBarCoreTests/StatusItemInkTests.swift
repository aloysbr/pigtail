import AppKit
import XCTest
@testable import MenuBarCore

/// How much of the 18 pt status-item canvas the glyph's *ink* actually
/// covers — and how that compares with the SF Symbols a real menu bar is
/// full of.
///
/// This file exists because of a defect the rest of the suite could not
/// see. Every test in `StatusItemImageTests` passed while the pig was
/// visibly punier than its neighbours in the bar: the image was 18×18, it
/// was a template, it drew more than 40 opaque pixels, the notch removed
/// ink. None of that says anything about how much of the canvas the
/// drawing fills, and the drawing filled 54%×60% of it. The comment in
/// `StatusItemImage` claiming the glyph "lives in the 16×16 centred" was an
/// intention nothing checked.
///
/// So: measure the ink, measure the system's own icons in the same canvas,
/// and assert against what was measured.
final class StatusItemInkTests: XCTestCase {

    // MARK: - Ink bounding box

    /// The tight box around every pixel whose alpha clears the threshold,
    /// expressed in points of the 18 pt canvas.
    struct InkBox {
        var minX: CGFloat
        var minY: CGFloat
        var maxX: CGFloat
        var maxY: CGFloat
        var width: CGFloat { maxX - minX }
        var height: CGFloat { maxY - minY }
        /// The dimension the eye reads as "how big is it" — an icon is as
        /// big as its longest side, which is why `battery.100` (6.5 pt
        /// tall) does not read as a small icon.
        var dominantSide: CGFloat { max(width, height) }
        var rect: NSRect { NSRect(x: minX, y: minY, width: width, height: height) }
    }

    /// Rasterizes `image` into the 18 pt canvas at `scale` and returns its
    /// ink box in points. `scale` is deliberately high (16 in the probes,
    /// and in the assertions) so the box is measured to a sixteenth of a
    /// point rather than to a whole one.
    static func inkBox(
        _ image: NSImage,
        scale: Int = probeScale,
        alphaThreshold: UInt8 = 8
    ) -> InkBox {
        let side = StatusItemImage.canvasSide
        let rep = StatusItemImageTests.bitmap(width: side, height: side, scale: scale)
        StatusItemImageTests.draw(into: rep) {
            image.draw(in: NSRect(x: 0, y: 0, width: side, height: side))
        }
        return inkBox(rep, alphaThreshold: alphaThreshold)
    }

    static func inkBox(_ rep: NSBitmapImageRep, alphaThreshold: UInt8 = 8) -> InkBox {
        guard let data = rep.bitmapData else {
            return InkBox(minX: 0, minY: 0, maxX: 0, maxY: 0)
        }
        let samples = rep.samplesPerPixel
        let scaleX = CGFloat(rep.pixelsWide) / rep.size.width
        let scaleY = CGFloat(rep.pixelsHigh) / rep.size.height
        var minPX = rep.pixelsWide, maxPX = -1
        var minPY = rep.pixelsHigh, maxPY = -1
        for y in 0..<rep.pixelsHigh {
            let row = data + y * rep.bytesPerRow
            for x in 0..<rep.pixelsWide where row[x * samples + 3] > alphaThreshold {
                if x < minPX { minPX = x }
                if x > maxPX { maxPX = x }
                if y < minPY { minPY = y }
                if y > maxPY { maxPY = y }
            }
        }
        guard maxPX >= 0 else { return InkBox(minX: 0, minY: 0, maxX: 0, maxY: 0) }
        // `NSBitmapImageRep`'s rows run top-down; the canvas is bottom-up.
        return InkBox(
            minX: CGFloat(minPX) / scaleX,
            minY: CGFloat(rep.pixelsHigh - 1 - maxPY) / scaleY,
            maxX: CGFloat(maxPX + 1) / scaleX,
            maxY: CGFloat(rep.pixelsHigh - minPY) / scaleY
        )
    }

    /// 16× the point size: 0.0625 pt of resolution on every edge of the
    /// box, which is finer than any difference this file argues about.
    static let probeScale = 16

    // MARK: - The reference: what the bar's own icons fill

    /// Symbols that genuinely show up in a menu bar. Not a taste
    /// judgement — the target of the calibration is what these cover.
    static let referenceSymbols = [
        "wifi", "battery.100", "magnifyingglass", "bolt.fill", "speaker.wave.2.fill",
    ]

    /// The point size the reference is quoted at. `NSStatusItem`'s square
    /// length is 18 pt, and a status item whose image is an SF Symbol is
    /// configured at the bar's own text size; 14…18 all give the same
    /// answer to within 1 pt (see `testProbeMeasureSystemSymbolInk`), so
    /// the exact choice does not carry the conclusion.
    static let referencePointSize: CGFloat = 18

    /// An SF Symbol rendered the way a status item renders one: configured
    /// at `pointSize`, then aspect-fit centred into the 18 pt canvas —
    /// what `NSStatusBarButton` does with an image wider or taller than the
    /// bar's thickness, and a no-op for one that already fits.
    static func symbolImage(_ name: String, pointSize: CGFloat = referencePointSize) -> NSImage? {
        guard let symbol = NSImage(systemSymbolName: name, accessibilityDescription: nil) else {
            return nil
        }
        let configured = symbol.withSymbolConfiguration(
            NSImage.SymbolConfiguration(pointSize: pointSize, weight: .regular)
        ) ?? symbol
        let side = StatusItemImage.canvasSide
        let natural = configured.size
        let factor = min(side / natural.width, side / natural.height, 1)
        let drawn = NSSize(width: natural.width * factor, height: natural.height * factor)
        let canvas = NSImage(size: NSSize(width: side, height: side), flipped: false) { _ in
            configured.draw(in: NSRect(
                x: (side - drawn.width) / 2,
                y: (side - drawn.height) / 2,
                width: drawn.width,
                height: drawn.height
            ))
            return true
        }
        canvas.isTemplate = true
        return canvas
    }

    // MARK: - The assertions
    //
    // The threshold, and why it is 0.75.
    //
    // Measured on this machine (`testProbeMeasureSystemSymbolInk`), the
    // dominant side of the five reference symbols' ink, across pointSize
    // 14…18, spans 13.75 pt (`speaker.wave.2.fill` at 14) to 15.44 pt
    // (`bolt.fill` at 17-18) — 76.4% to 85.8% of the 18 pt canvas. The
    // floor of that range is 13.75 pt, so the assertion is set just below
    // it, at 0.75 × 18 = 13.5 pt: anything that clears it is inside the
    // band the system's own icons occupy, and the glyph that prompted this
    // file (10.75 pt, 59.7%) fails it by 2.75 pt.
    //
    // A ceiling is not expressed as a percentage but as containment in the
    // 16×16 box: an icon cannot be too big by a little, it can only run out
    // of breathing room and then get clipped.

    /// The floor: a fraction of the canvas the glyph's longest side must
    /// cover for it to read as one of the bar's icons rather than as a
    /// small thing floating in a slot.
    static let minimumInkFill: CGFloat = 0.75

    /// **The test that was missing.** Had it existed, the 54%×60% glyph
    /// would never have reached the user's menu bar.
    func testTheGlyphsInkFillsTheCanvasLikeTheBarsOwnIcons() {
        let floor = Self.minimumInkFill * StatusItemImage.canvasSide
        let box = Self.inkBox(StatusItemImage.base())
        XCTAssertGreaterThanOrEqual(
            box.dominantSide,
            floor,
            """
            ink is \
            \(String(format: "%.2f", box.width))×\
            \(String(format: "%.2f", box.height)) pt, dominant side \
            \(String(format: "%.2f", box.dominantSide)) pt — under the \
            \(String(format: "%.2f", floor)) pt floor the bar's own SF Symbols \
            set (13.75…15.44 pt measured). It will read as undersized next to them.
            """
        )
    }

    /// The ceiling, and the reason `glyphSide` exists: the ink has to stay
    /// inside the 16×16 centred in the canvas. Clipping is the failure
    /// this catches — an ink box touching the canvas edge means the
    /// drawing was cut off, and 1 pt of margin is what keeps the glyph
    /// from crowding its neighbours in the bar.
    func testTheGlyphsInkStaysInsideTheCentredGlyphBox() {
        let inset = (StatusItemImage.canvasSide - StatusItemImage.glyphSide) / 2
        let box = NSRect(
            x: inset, y: inset,
            width: StatusItemImage.glyphSide, height: StatusItemImage.glyphSide
        )
        let ink = Self.inkBox(StatusItemImage.base())
        XCTAssertTrue(
            box.contains(ink.rect),
            """
            ink box \
            x[\(String(format: "%.2f", ink.minX))…\(String(format: "%.2f", ink.maxX))] \
            y[\(String(format: "%.2f", ink.minY))…\(String(format: "%.2f", ink.maxY))] \
            escapes the \(StatusItemImage.glyphSide)×\(StatusItemImage.glyphSide) centred box
            """
        )
    }

    /// Variant D is fitted by its longest side: the ink is exactly
    /// `glyphSide` across, to the probe's resolution.
    func testTheGlyphsLongestSideFillsTheGlyphBox() {
        let ink = Self.inkBox(StatusItemImage.base())
        XCTAssertEqual(ink.dominantSide, StatusItemImage.glyphSide, accuracy: 1 / CGFloat(Self.probeScale))
    }

    /// Centred on its own ink box, in x and in y.
    func testTheGlyphIsCentredOnItsInk() {
        let ink = Self.inkBox(StatusItemImage.base())
        let half = StatusItemImage.canvasSide / 2
        XCTAssertEqual(ink.rect.midX, half, accuracy: 1 / CGFloat(Self.probeScale))
        XCTAssertEqual(ink.rect.midY, half, accuracy: 1 / CGFloat(Self.probeScale))
    }

    /// The reference itself, asserted rather than trusted. If a future
    /// macOS redraws `magnifyingglass` at half the size, the floor above
    /// stops meaning what this file says it means, and this is what says
    /// so out loud.
    func testTheReferenceSymbolsStillFillWhatTheFloorAssumes() {
        let floor = Self.minimumInkFill * StatusItemImage.canvasSide
        for name in Self.referenceSymbols {
            guard let image = Self.symbolImage(name) else {
                XCTFail("\(name): no such system symbol on this OS")
                continue
            }
            let box = Self.inkBox(image)
            XCTAssertGreaterThanOrEqual(
                box.dominantSide,
                floor,
                "\(name): dominant side \(String(format: "%.2f", box.dominantSide)) pt"
            )
        }
    }

    // MARK: - The probes
    //
    // These assert nothing; they print the measurements the thresholds
    // above were chosen from, so the next person to touch the geometry can
    // re-derive them in one command instead of re-inventing the
    // instrument:
    //
    //     swift test --package-path macapp --filter StatusItemInkTests/testProbe

    func testProbeMeasureThePigsInk() {
        print("--- pig ink box, canvas \(StatusItemImage.canvasSide) pt, raster \(Self.probeScale)x ---")
        let box = Self.inkBox(StatusItemImage.base())
        print(String(
            format: "x[%.3f…%.3f] y[%.3f…%.3f]  w=%.3f h=%.3f  fill w=%.1f%% h=%.1f%%",
            box.minX, box.maxX, box.minY, box.maxY,
            box.width, box.height,
            100 * box.width / StatusItemImage.canvasSide,
            100 * box.height / StatusItemImage.canvasSide
        ))
    }

    func testProbeMeasureSystemSymbolInk() {
        print("--- SF Symbol ink box, same 18 pt canvas, raster \(Self.probeScale)x ---")
        for pointSize in [14, 15, 16, 17, 18] as [CGFloat] {
            for name in Self.referenceSymbols {
                guard let symbol = NSImage(systemSymbolName: name, accessibilityDescription: nil),
                      let image = Self.symbolImage(name, pointSize: pointSize) else {
                    print("\(name): unavailable")
                    continue
                }
                let natural = symbol.withSymbolConfiguration(
                    NSImage.SymbolConfiguration(pointSize: pointSize, weight: .regular)
                )?.size ?? .zero
                let box = Self.inkBox(image)
                print(String(
                    format: "pt=%.0f %-20@ natural=%.2f×%.2f  ink w=%.3f h=%.3f  fill w=%.1f%% h=%.1f%%",
                    pointSize, name, natural.width, natural.height,
                    box.width, box.height,
                    100 * box.width / StatusItemImage.canvasSide,
                    100 * box.height / StatusItemImage.canvasSide
                ))
            }
        }
    }
}
