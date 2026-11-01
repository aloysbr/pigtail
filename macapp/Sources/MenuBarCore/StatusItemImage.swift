import AppKit

/// Draws the menu-bar status item's glyph, in code.
///
/// **No SwiftPM resource, ever.** The `app` target of the `Makefile`
/// copies the binary, `Info.plist` and the app icon
/// (`macapp/Resources/AppIcon.icns`, the Finder and notification icon, not
/// this glyph) into the bundle; it has no SwiftPM `Resources/` step. A SwiftPM
/// resource would be built into `.build/release/Pigtail_MenuBarCore.bundle`,
/// never copied, and `Bundle.module` would `fatalError` at the first access —
/// in an `LSUIElement` app, a launch crash with no window to show it. So the
/// glyph is polygon data (`StatusItemGlyph`, generated) filled with
/// `NSBezierPath`, not a PDF, an asset catalog or a `.symbolset`.
///
/// **Template, not a bitmap.** `base()` returns an image with
/// `isTemplate = true`: AppKit then tints it for the current appearance —
/// light bar, dark bar — and inverts it while the status item is
/// highlighted (clicked). The drawing handler is re-invoked by AppKit for
/// every backing scale, so the polygons are resolution-independent; nothing
/// here rasterizes anything.
///
/// **How this glyph was chosen.** S0, a head measured from a silhouette
/// (PR #107, its losing candidates in `refs/pull/107/head`), shipped first.
/// On 2026-10-27 it was replaced by variant D of mockup t23: the head of the
/// app icons — smiling slit eyes under an m30 lid, left ear drooping,
/// superellipse 2.75 snout, drop nostrils, head turned 4° — without the tail
/// and the smile, which do not survive 16 pt. Source and regeneration: the
/// header of `StatusItemGlyph.swift`.
public enum StatusItemImage {

    // MARK: - Metrics
    //
    // A menu-bar status item is 18 pt tall at `NSStatusItem.squareLength`;
    // the glyph lives in the 16×16 centred inside that canvas, leaving the
    // 1 pt of breathing room the bar's own icons keep. `StatusItemInkTests`
    // rasterizes the image and asserts the ink sits inside that box, fills
    // it on its longest side, and is centred. The SF Symbols of a real menu
    // bar cover 13.75…15.44 pt in the same canvas; variant D's ink measures
    // 16.0 × 14.5 pt.

    /// The image's side, in points.
    public static let canvasSide: CGFloat = 18
    /// The side of the centred box the glyph is inscribed in.
    public static let glyphSide: CGFloat = 16

    /// Points per u: the ink's longest side fills `glyphSide` (≈ 6.5575).
    static let inkScale: CGFloat = glyphSide
        / max(StatusItemGlyph.inkBounds.width, StatusItemGlyph.inkBounds.height)

    // MARK: - The template image

    /// The status item's base image: the pig's head, monochrome, always bare (no badge, D9).
    public static func base() -> NSImage {
        let size = NSSize(width: canvasSide, height: canvasSide)
        let image = NSImage(size: size, flipped: false) { _ in
            // Template images carry shape, not colour; black gives the mask full alpha.
            NSColor.black.setFill()
            for path in glyphPaths() { path.fill() }
            return true
        }
        image.isTemplate = true
        return image
    }

    /// One even-odd path per polygon of `StatusItemGlyph`, in canvas points,
    /// the ink box centred in the canvas. The rings after a polygon's first
    /// are its holes (eyes, the snout's ring, nostrils); the snout disc is
    /// its own polygon, filled inside the skull's ring hole.
    static func glyphPaths() -> [NSBezierPath] {
        let box = StatusItemGlyph.inkBounds
        let s = inkScale
        func p(_ x: Double, _ y: Double) -> CGPoint {
            CGPoint(x: canvasSide / 2 + (CGFloat(x) - box.midX) * s,
                    y: canvasSide / 2 + (CGFloat(y) - box.midY) * s)
        }
        return StatusItemGlyph.polygons.map { rings in
            let path = NSBezierPath()
            path.windingRule = .evenOdd
            for ring in rings {
                path.move(to: p(ring[0], ring[1]))
                for i in stride(from: 2, to: ring.count, by: 2) {
                    path.line(to: p(ring[i], ring[i + 1]))
                }
                path.close()
            }
            return path
        }
    }
}

// MARK: - Travel Mode: the text next to the head (D23)
//
// While Travel Mode is on the status item shows the head (this template
// image, unchanged) and, to its right, `LidStatusTitle.title` as the button's
// own title: `imagePosition = .imageLeft`, length `variableLength`. AppKit then
// lays the two out, so the image is no longer centred in the bounds.
extension StatusItemImage {
    /// The NSStatusItem's length: 24 pt fixed for the head alone (18 + 3 pt each
    /// side), `NSStatusItem.variableLength` when a title sits next to it.
    public static func statusItemLength(title: String?) -> CGFloat {
        title == nil ? 24 : NSStatusItem.variableLength
    }
}
