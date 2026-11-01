import AppKit
import XCTest
@testable import MenuBarCore

/// D23: the status item's length with and without the Travel Mode text.
final class StatusItemTitleLayoutTests: XCTestCase {
    func testLengthIsFixedWithoutTitleAndVariableWithIt() {
        XCTAssertEqual(StatusItemImage.statusItemLength(title: nil), 24)
        XCTAssertEqual(StatusItemImage.statusItemLength(title: "Travel Mode 3\u{00A0}h"), NSStatusItem.variableLength)
        XCTAssertEqual(StatusItemImage.statusItemLength(title: "Travel Mode"), NSStatusItem.variableLength)
    }
}
