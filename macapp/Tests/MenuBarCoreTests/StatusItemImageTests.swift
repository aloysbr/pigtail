import AppKit
import XCTest
@testable import MenuBarCore

final class StatusItemImageTests: XCTestCase {

    // MARK: - Contract

    func testTheGlyphIsDrawnAtTheStatusItemSize() {
        XCTAssertEqual(StatusItemImage.base().size, NSSize(width: 18, height: 18))
    }

    func testTheGlyphIsATemplateSoTheBarCanTintAndInvertIt() {
        XCTAssertTrue(StatusItemImage.base().isTemplate)
    }

    func testTheGlyphActuallyDrawsSomething() {
        // A drawing handler that silently draws nothing still yields a
        // valid 18×18 image — this is the assertion that tells the two
        // apart.
        XCTAssertGreaterThan(Self.opaquePixelCount(Self.raster(StatusItemImage.base(), scale: 2)), 40)
    }

    // MARK: - Rasterizing (test-side only)

    /// Draws `image` into a bitmap `scale` times the point size.
    static func raster(_ image: NSImage, scale: Int) -> NSBitmapImageRep {
        let side = StatusItemImage.canvasSide
        let rep = Self.bitmap(width: side, height: side, scale: scale)
        Self.draw(into: rep) {
            image.draw(in: NSRect(x: 0, y: 0, width: side, height: side))
        }
        return rep
    }

    static func bitmap(width: CGFloat, height: CGFloat, scale: Int) -> NSBitmapImageRep {
        let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: Int(width.rounded()) * scale,
            pixelsHigh: Int(height.rounded()) * scale,
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bytesPerRow: 0,
            bitsPerPixel: 0
        )!
        // Points, not pixels: the drawing below stays scale-independent.
        rep.size = NSSize(width: width, height: height)
        return rep
    }

    static func draw(into rep: NSBitmapImageRep, _ body: () -> Void) {
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        body()
        NSGraphicsContext.current?.flushGraphics()
        NSGraphicsContext.restoreGraphicsState()
    }

    static func pixels(_ rep: NSBitmapImageRep) -> Data {
        guard let data = rep.bitmapData else { return Data() }
        return Data(bytes: data, count: rep.bytesPerRow * rep.pixelsHigh)
    }

    static func opaquePixelCount(_ rep: NSBitmapImageRep) -> Int {
        guard let data = rep.bitmapData else { return 0 }
        var count = 0
        let samples = rep.samplesPerPixel
        for y in 0..<rep.pixelsHigh {
            let row = data + y * rep.bytesPerRow
            for x in 0..<rep.pixelsWide where row[x * samples + 3] > 8 {
                count += 1
            }
        }
        return count
    }
}
