import XCTest
import SwiftUI
import AppKit
@testable import MenuBarCore

final class PanelSizePolicyTests: XCTestCase {
    private func measure(_ h: CGFloat, current: CGFloat, target: CGFloat) -> PanelMeasure {
        PanelMeasure(size: CGSize(width: 360, height: h), fold: FoldHeights(current: current, target: target))
    }

    func testOpeningTakesTheFinalHeightFromTheFirstFrame() {
        // closed card 132, fold 0 → 192: the window is 324 at once, whatever the animated frame says
        for (h, c) in [(132.0, 0.0), (160, 28), (324, 192)] {
            let d = PanelSizePolicy.decide(measure(h, current: c, target: 192), windowHeight: 132)
            XCTAssertEqual(d.size.height, 324, "h=\(h) current=\(c)")
            XCTAssertNil(d.settleAfter)
        }
    }

    func testClosingKeepsTheOpenHeightThenSettles() {
        let d = PanelSizePolicy.decide(measure(300, current: 168, target: 0), windowHeight: 324)
        XCTAssertEqual(d.size.height, 324)
        XCTAssertEqual(d.settleAfter, AccordionMotion.duration + 0.05)
    }

    func testSteadyAndReducedMotionAreImmediate() {
        // reduced motion: no animation, the fold's current equals its target in the same frame
        XCTAssertEqual(PanelSizePolicy.decide(measure(132, current: 0, target: 0), windowHeight: 324).size.height, 132)
        XCTAssertEqual(PanelSizePolicy.decide(measure(324, current: 192, target: 192), windowHeight: 132).size.height, 324)
        XCTAssertNil(PanelSizePolicy.decide(measure(132, current: 0, target: 0), windowHeight: 324).settleAfter)
    }

    func testPreOpenIsTheOpenCardSizeAndKeepsTheTop() {
        let m = PanelMeasure(size: CGSize(width: 360, height: 132), fold: FoldHeights(current: 0, target: 0, natural: 192))
        XCTAssertEqual(PanelSizePolicy.preOpen(m), CGSize(width: 360, height: 324))
    }

    @MainActor
    func testPreparingOnlyActsOnOpening() {
        var window: CGFloat = 132
        let d = PanelSizeDriver(apply: { window = $0.height }, windowHeight: { window })
        var value = false
        let b = d.preparing(Binding(get: { value }, set: { value = $0 }))
        d.report(PanelMeasure(size: CGSize(width: 360, height: 132), fold: FoldHeights(current: 0, target: 0, natural: 192)))
        b.wrappedValue = true
        XCTAssertEqual(window, 324)
        XCTAssertTrue(value)
        window = 200
        b.wrappedValue = false
        XCTAssertEqual(window, 200)
    }

    func testWidthFollowsTheCard() {
        XCTAssertEqual(PanelSizePolicy.decide(measure(132, current: 0, target: 10), windowHeight: 132).size.width, 360)
    }

    // MARK: driver

    @MainActor
    func testDriverHoldsTheWindowUntilTheCloseSettles() {
        var heights: [CGFloat] = []
        var window: CGFloat = 132
        var timers: [() -> Void] = []
        var cancelled = 0
        let d = PanelSizeDriver(apply: { window = $0.height; heights.append($0.height) },
                                windowHeight: { window },
                                schedule: { _, work in timers.append(work); return { cancelled += 1 } })
        d.report(measure(132, current: 0, target: 192))      // open
        XCTAssertEqual(window, 324)
        d.report(measure(324, current: 192, target: 192))
        d.report(measure(300, current: 168, target: 0))      // close starts
        d.report(measure(200, current: 68, target: 0))
        d.report(measure(132, current: 0, target: 0))        // animation done, timer not yet
        XCTAssertEqual(window, 324)
        XCTAssertEqual(timers.count, 1)
        timers[0]()
        XCTAssertEqual(window, 132)
        d.report(measure(140, current: 0, target: 0))        // a refresh afterwards applies at once
        XCTAssertEqual(window, 140)
    }

    @MainActor
    func testDriverReopeningDuringTheHoldCancelsTheShrink() {
        var window: CGFloat = 324
        var timers: [() -> Void] = []
        var cancelled = 0
        let d = PanelSizeDriver(apply: { window = $0.height }, windowHeight: { window },
                                schedule: { _, work in timers.append(work); return { cancelled += 1 } })
        d.report(measure(300, current: 168, target: 0))
        d.report(measure(250, current: 118, target: 192))    // reopened
        XCTAssertEqual(cancelled, 1)
        XCTAssertEqual(window, 324)
    }
}

/// The real path, window off screen: a SwiftUI card in an NSHostingController in a transparent NSPanel,
/// sized by PanelSizeDriver, top edge pinned. The card's top, in screen coordinates, must not move
/// while the detail fold opens and closes (the reviewer's check: only the bottom opens, top to bottom).
@MainActor
final class DetailFoldAnchorTests: XCTestCase {
    final class Box: ObservableObject { @Published var open = false }
    final class MarkerView: NSView { override var isFlipped: Bool { true } }
    struct Marker: NSViewRepresentable {
        let sink: (NSView) -> Void
        func makeNSView(context: Context) -> NSView { let v = MarkerView(); sink(v); return v }
        func updateNSView(_ nsView: NSView, context: Context) {}
    }

    struct Card: View {
        @ObservedObject var box: Box
        let sink: (NSView) -> Void
        let onMeasure: (PanelMeasure) -> Void
        var body: some View {
            VStack(spacing: 0) {
                Marker(sink: sink).frame(height: 2)
                Text("pigtail").padding(8)
                Divider()
                AccordionDisclosure(expanded: $box.open, capped: false) { Text("Détail par compte") } content: {
                    ForEach(0..<8, id: \.self) { Text("ligne \($0)").frame(height: 24) }
                }
                Divider()
                Text("Quitter").padding(8)
            }
            .frame(width: 320)
            .background(Color.gray)
            .padding(20)
            .reportsPanelMeasure(onMeasure)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        }
    }

    /// `useDriver: false` is the former behaviour: the window mirrors every reported size at once.
    private func run(useDriver: Bool) -> (markerTops: [CGFloat], heights: [CGFloat], finalClosed: CGFloat, open: CGFloat) {
        let box = Box()
        var marker: NSView?
        var panel: NSPanel!
        var top: CGFloat = 0
        func setSize(_ s: CGSize) {
            guard s.height > 1 else { return }
            let f = NSRect(x: panel.frame.minX, y: top - s.height, width: s.width, height: s.height)
            if f != panel.frame { panel.setFrame(f, display: true); panel.contentView?.layoutSubtreeIfNeeded() }
        }
        let driver = PanelSizeDriver(apply: setSize, windowHeight: { panel.frame.height })
        let card = Card(box: box, sink: { marker = $0 },
                        onMeasure: { useDriver ? driver.report($0) : setSize($0.size) })
        let hosting = NSHostingController(rootView: card)
        hosting.sizingOptions = []
        panel = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 360, height: 100),
                        styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        panel.contentViewController = hosting
        panel.setContentSize(NSSize(width: 360, height: 132))
        panel.setFrameOrigin(NSPoint(x: -6000, y: 500))      // off screen, invisible, no mouse
        panel.alphaValue = 0
        panel.ignoresMouseEvents = true
        panel.orderFrontRegardless()
        top = panel.frame.maxY
        func spin(_ seconds: TimeInterval) { RunLoop.main.run(until: Date().addingTimeInterval(seconds)) }
        spin(0.1)
        func markerTop() -> CGFloat {
            guard let m = marker, let w = m.window else { return -1 }
            return w.frame.origin.y + m.convert(m.bounds, to: nil).maxY
        }
        var tops = [markerTop()]
        var heights = [panel.frame.height]
        func sample(_ seconds: TimeInterval) {
            let start = Date()
            while Date().timeIntervalSince(start) < seconds {
                spin(1.0 / 60)
                tops.append(markerTop()); heights.append(panel.frame.height)
            }
        }
        // The caller's binding, as the popover builds it: opening first grows the window (useDriver), then animates.
        let open = useDriver ? driver.preparing(Binding(get: { box.open }, set: { box.open = $0 }))
                             : Binding(get: { box.open }, set: { box.open = $0 })
        withAnimation(AccordionMotion.animation(reduceMotion: false)) { open.wrappedValue = true }
        sample(0.45)
        let openHeight = panel.frame.height
        withAnimation(AccordionMotion.animation(reduceMotion: false)) { box.open = false }
        sample(0.5)
        let closed = panel.frame.height
        panel.orderOut(nil)
        return (tops, heights, closed, openHeight)
    }

    func testCardTopNeverMovesWhileTheDetailFoldAnimates() {
        let r = run(useDriver: true)
        let first = r.markerTops[0]
        XCTAssertGreaterThan(first, 0)
        XCTAssertEqual(r.markerTops.min()!, first, accuracy: 0.5, "tops: \(r.markerTops)")
        XCTAssertEqual(r.markerTops.max()!, first, accuracy: 0.5, "tops: \(r.markerTops)")
        XCTAssertGreaterThan(r.open, r.finalClosed + 100)
        XCTAssertEqual(r.finalClosed, r.heights[0], accuracy: 0.5, "the window shrinks back once the fold is closed")
    }

    func testFormerWindowSizingMovedTheCard() {
        let r = run(useDriver: false)
        XCTAssertGreaterThan(r.markerTops.max()! - r.markerTops.min()!, 5, "defect reproduced: the card slides")
    }
}
