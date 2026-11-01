import XCTest
import SwiftUI
@testable import MenuBarCore

/// The motif StartDisclosure proved (and DisclosureGroup failed: it reserves its whole height at
/// once and makes the resized panel jump): collapsed = the header only; expanded = an explicit
/// height, capped, content scrolling inside.
@MainActor
final class AccordionDisclosureTests: XCTestCase {
    private func height(expanded: Bool) -> CGFloat {
        let view = AccordionDisclosure(expanded: expanded, maxContentHeight: 160) {
            Text("En attente (12)")
        } content: {
            ForEach(0..<12, id: \.self) { Text("ligne \($0)").frame(height: 24) }
        }
        .frame(width: 320)
        return settledHeight(view)
    }

    /// The height once the content height is measured. The fold measures its content through a
    /// preference (CappedScrollView), which lands after a layout pass in a window — as in the popover,
    /// where the content is measured while the fold is still closed. A bare `fittingSize` on a
    /// windowless host reads the first pass only, before that measure.
    private func settledHeight<V: View>(_ view: V) -> CGFloat {
        let host = NSHostingView(rootView: view)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 320, height: 600),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.contentView = host
        for _ in 0..<3 {
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        }
        return host.fittingSize.height
    }

    func testCollapsedReservesOnlyTheHeader() {
        XCTAssertLessThan(height(expanded: false), 40, "a collapsed repli must not reserve its content's height")
    }

    func testExpandedIsCappedAtMaxContentHeight() {
        let collapsed = height(expanded: false), expanded = height(expanded: true)
        XCTAssertGreaterThan(expanded, collapsed + 100)
        XCTAssertLessThanOrEqual(expanded, collapsed + 160 + 1, "12 x 24 pt of content must scroll inside the 160 pt cap")
    }

    /// Verify N2 r1: a closed fold whose rows carried the space under the block ends flush on the
    /// next separator. The fold carries that space itself when closed, and adds nothing when open
    /// (the last row's own padding is the space then, as before D9).
    private func height(expanded: Bool, collapsedBottomInset: CGFloat) -> CGFloat {
        let view = AccordionDisclosure(expanded: expanded, maxContentHeight: 160,
                                       collapsedBottomInset: collapsedBottomInset) {
            Text("Serveurs (2)")
        } content: {
            ForEach(0..<2, id: \.self) { Text("ligne \($0)").frame(height: 24) }
        }
        .frame(width: 320)
        return settledHeight(view)
    }

    func testCollapsedBottomInsetIsCarriedByTheClosedFoldOnly() {
        XCTAssertEqual(height(expanded: false, collapsedBottomInset: 6),
                       height(expanded: false, collapsedBottomInset: 0) + 6, accuracy: 0.5,
                       "closed: the space under the block is the fold's own")
        XCTAssertEqual(height(expanded: true, collapsedBottomInset: 6),
                       height(expanded: true, collapsedBottomInset: 0), accuracy: 0.5,
                       "open: the rows' own padding is the space, nothing added")
    }

    // MARK: - accessibility (verify N2 r2)

    /// A source test, not an introspection: in a test process the SwiftUI accessibility tree of an
    /// NSHostingView comes back empty (measured 2026-10-08: the root element alone, no label, no
    /// child, even with the open content — the positive control failed), so the tree cannot be read
    /// here. What is proven instead: the words are the decided ones, and the view wires them — the
    /// closed content is hidden from accessibility on the scrolling branch (the one the popover
    /// uses), and the header button's value follows the state. Falls when either modifier is removed.
    func testClosedContentIsHiddenFromAccessibilityAndTheHeaderSaysItsState() throws {
        XCTAssertEqual(AccordionAccessibility.value(expanded: true), "déplié")
        XCTAssertEqual(AccordionAccessibility.value(expanded: false), "replié")

        let source = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()   // macapp/
            .appendingPathComponent("Sources/MenuBarCore/AccordionDisclosure.swift")
        let code = try String(contentsOf: source, encoding: .utf8)
            .components(separatedBy: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.hasPrefix("//") }
        XCTAssertGreaterThan(code.count, 40, "instrument control: the source was not read")
        func index(_ needle: String) -> Int? { code.firstIndex { $0.contains(needle) } }
        let clipped = try XCTUnwrap(index(".clipped()"), "instrument control: the scrolling branch was not found")
        let hidden = try XCTUnwrap(index(".accessibilityHidden(!expanded)"),
                                   "a closed fold's content stays reachable by VoiceOver")
        XCTAssertEqual(hidden, clipped + 1, "the content's accessibilityHidden must follow its .clipped()")
        let plain = try XCTUnwrap(index(".buttonStyle(.plain)"), "instrument control: the header button was not found")
        let value = try XCTUnwrap(index(".accessibilityValue(AccordionAccessibility.value(expanded: expanded))"),
                                  "the header button does not say whether the fold is open")
        XCTAssertEqual(value, plain + 1, "the header's accessibilityValue must follow its .buttonStyle(.plain)")
    }

    // MARK: - uncapped, animated mode and binding (Lot 3)

    private final class Box: ObservableObject { @Published var open = false }

    private struct BoundFold: View {
        @ObservedObject var box: Box
        let capped: Bool
        var body: some View {
            AccordionDisclosure(expanded: $box.open, capped: capped, maxContentHeight: 160) {
                Text("Détail par compte")
            } content: {
                ForEach(0..<12, id: \.self) { Text("ligne \($0)").frame(height: 24) }
            }
            .frame(width: 320)
        }
    }

    private func settle(_ host: NSHostingView<some View>) -> CGFloat {
        for _ in 0..<3 {
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        }
        return host.fittingSize.height
    }

    private func mount<V: View>(_ view: V) -> NSHostingView<V> {
        let host = NSHostingView(rootView: view)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 320, height: 800),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.contentView = host
        return host
    }

    func testBindingDrivesExpansion() {
        let box = Box()
        let host = mount(BoundFold(box: box, capped: true))
        let closed = settle(host)
        XCTAssertLessThan(closed, 40)
        box.open = true
        let open = settle(host)
        XCTAssertGreaterThan(open, closed + 100, "the binding opened the fold")
        box.open = false
        XCTAssertEqual(settle(host), closed, accuracy: 0.5, "and closed it again")
    }

    /// The detail has no cap: 12 x 24 pt rows open to their whole height (beyond the 160 pt default),
    /// closed to 0 while the measure stays, so reopening needs no new pass.
    func testUncappedModeIsAnimatedBranch() {
        let box = Box()
        let host = mount(BoundFold(box: box, capped: false))
        let closed = settle(host)
        XCTAssertLessThan(closed, 40, "closed: the header only")
        box.open = true
        let open = settle(host)
        XCTAssertGreaterThan(open, closed + 12 * 24 - 2, "open: the whole content, no 160 pt cap, no inner scrolling")
        XCTAssertLessThanOrEqual(open, closed + 12 * 24 + 2)
        box.open = false
        XCTAssertEqual(settle(host), closed, accuracy: 0.5)
        box.open = true
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        XCTAssertGreaterThan(host.fittingSize.height, closed + 12 * 24 - 2, "reopening reads the kept measure")
        // control: the capped branch stops at 160 pt for the same content
        let capped = Box(); capped.open = true
        XCTAssertLessThanOrEqual(settle(mount(BoundFold(box: capped, capped: true))), closed + 160 + 1)
    }

    func testDisclosureAnimationDuration() throws {
        XCTAssertEqual(AccordionMotion.duration, 0.22)
        XCTAssertEqual(AccordionMotion.animation(reduceMotion: false), .easeInOut(duration: 0.22))
        let code = try Self.sourceLines()
        // the toggle and the uncapped branch both use that one Animation
        XCTAssertTrue(code.contains { $0.contains("AccordionMotion.animation(reduceMotion: reduceMotion)") },
                      "the header toggle must animate with AccordionMotion")
        let frame = try XCTUnwrap(code.firstIndex { $0.contains(".frame(height: expanded ? measured : 0, alignment: .top)") },
                                  "the uncapped branch animates an explicit frame(height:)")
        XCTAssertTrue(code[frame...].prefix(3).contains { $0.contains(".clipped()") })
    }

    func testReduceMotionNoHeightAnimation() throws {
        XCTAssertNil(AccordionMotion.animation(reduceMotion: true), "G10: no animation under reduced motion")
        let code = try Self.sourceLines()
        XCTAssertTrue(code.contains { $0.contains("@Environment(\\.accessibilityReduceMotion)") },
                      "the fold reads the accessibility setting")
        XCTAssertFalse(code.contains { $0.contains("withAnimation(.easeInOut") },
                       "no hard-coded animation left that would ignore reduced motion")
    }

    private static func sourceLines() throws -> [String] {
        let source = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources/MenuBarCore/AccordionDisclosure.swift")
        let code = try String(contentsOf: source, encoding: .utf8)
            .components(separatedBy: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.hasPrefix("//") }
        XCTAssertGreaterThan(code.count, 40, "instrument control: the source was not read")
        return code
    }
}
