import XCTest
@testable import MenuBarCore

/// The plan's contrast rule: text is at least 4.5:1 on the popover (light: white; dark: the mockup's
/// popover over its wall, `viewer.css:52`, about #262629).
final class BarPaletteTests: XCTestCase {
    private func luminance(_ hex: UInt32) -> Double {
        func lin(_ v: UInt32) -> Double {
            let c = Double(v) / 255
            return c <= 0.03928 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * lin((hex >> 16) & 0xFF) + 0.7152 * lin((hex >> 8) & 0xFF) + 0.0722 * lin(hex & 0xFF)
    }
    private func contrast(_ a: UInt32, _ b: UInt32) -> Double {
        let (x, y) = (luminance(a), luminance(b))
        return (max(x, y) + 0.05) / (min(x, y) + 0.05)
    }

    func testSecondaryTextLightReaches45OnWhite() {
        XCTAssertGreaterThanOrEqual(contrast(BarPalette.textSecondaryLightHex, 0xFFFFFF), 4.5)
    }

    func testSecondaryTextDarkReaches45OnMockupPopover() {
        XCTAssertGreaterThanOrEqual(contrast(BarPalette.textSecondaryDarkHex, 0x262629), 4.5)
    }
}
