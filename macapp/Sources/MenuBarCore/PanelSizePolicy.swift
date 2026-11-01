import SwiftUI

/// What the uncapped folds report about their height, summed: `current` is the animated height on
/// screen this frame, `target` the height the state settles on (0 closed, the measured height open).
public struct FoldHeights: Equatable, Sendable {
    public var current: CGFloat
    public var target: CGFloat
    /// The content's own height, whatever the state: what opening the fold adds.
    public var natural: CGFloat
    public init(current: CGFloat = 0, target: CGFloat = 0, natural: CGFloat = 0) {
        self.current = current; self.target = target; self.natural = natural
    }
}

/// One frame of what the popover card measures: its size (shadow margin included) and its folds.
public struct PanelMeasure: Equatable, Sendable {
    public var size: CGSize
    public var fold: FoldHeights
    public init(size: CGSize, fold: FoldHeights = FoldHeights()) { self.size = size; self.fold = fold }
}

/// One preference carries both readings so they reach the host from the same layout pass: the card
/// emits its size (folds emit `.zero`), each fold emits its heights (the card emits none); they merge here.
public struct PanelMeasureKey: PreferenceKey {
    public static let defaultValue = PanelMeasure(size: .zero)
    public static func reduce(value: inout PanelMeasure, nextValue: () -> PanelMeasure) {
        let n = nextValue()
        value = PanelMeasure(size: n.size == .zero ? value.size : n.size,
                             fold: FoldHeights(current: value.fold.current + n.fold.current,
                                               target: value.fold.target + n.fold.target,
                                               natural: value.fold.natural + n.fold.natural))
    }
}

extension View {
    /// Reports the view's size and, from the same layout pass, the heights of the folds inside it.
    public func reportsPanelMeasure(_ onChange: @escaping (PanelMeasure) -> Void) -> some View {
        background(GeometryReader { proxy in
            Color.clear.preference(key: PanelMeasureKey.self, value: PanelMeasure(size: proxy.size))
        })
        .onPreferenceChange(PanelMeasureKey.self) { onChange($0) }
    }
}

/// How tall the transparent window is while a fold animates: the card's top never moves, so the
/// window must already be as tall as the card will be when a fold opens, and must stay that tall
/// until the fold has finished closing. Pure: the window and the clock belong to `PanelSizeDriver`.
public enum PanelSizePolicy {
    /// Time to wait after a close starts before the window may shrink: the fold's motion plus a margin.
    public static let settleDelay: TimeInterval = AccordionMotion.duration + 0.05

    public struct Decision: Equatable, Sendable {
        public var size: CGSize
        /// Non-nil: a fold is closing, the window keeps this size and shrinks to the latest measure after the delay.
        public var settleAfter: TimeInterval?
    }

    /// The window size to take just before a fold starts to open: the card's size once open.
    /// Taken before the animation starts, not during it: a window that grows while the animation runs
    /// makes the card slide (measured: the card starts mid-window and glides up to the top).
    public static func preOpen(_ m: PanelMeasure) -> CGSize {
        CGSize(width: m.size.width, height: m.size.height - m.fold.current + m.fold.natural)
    }

    /// `windowHeight` is the window's current height. With reduced motion a fold's `current` equals its
    /// `target` at once, so a toggle is immediate by construction.
    public static func decide(_ m: PanelMeasure, windowHeight: CGFloat) -> Decision {
        let f = m.fold
        if f.target > f.current {            // opening: final height from the first frame
            return Decision(size: CGSize(width: m.size.width, height: m.size.height - f.current + f.target), settleAfter: nil)
        }
        if f.target < f.current {            // closing: stay as tall as now, shrink at the end
            return Decision(size: CGSize(width: m.size.width, height: max(windowHeight, m.size.height)),
                            settleAfter: settleDelay)
        }
        return Decision(size: m.size, settleAfter: nil)
    }
}

/// Applies `PanelSizePolicy` to a window: `apply` sets the window size (top edge pinned by the owner).
@MainActor
public final class PanelSizeDriver {
    public typealias Cancel = () -> Void
    private let apply: (CGSize) -> Void
    private let windowHeight: () -> CGFloat
    private let schedule: (TimeInterval, @escaping () -> Void) -> Cancel
    private var latest: PanelMeasure?
    private var cancelSettle: Cancel?

    public init(apply: @escaping (CGSize) -> Void,
                windowHeight: @escaping () -> CGFloat,
                schedule: @escaping (TimeInterval, @escaping () -> Void) -> Cancel = PanelSizeDriver.mainQueueSchedule) {
        self.apply = apply; self.windowHeight = windowHeight; self.schedule = schedule
    }

    nonisolated public static func mainQueueSchedule(_ delay: TimeInterval, _ work: @escaping () -> Void) -> Cancel {
        let item = DispatchWorkItem(block: work)
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: item)
        return { item.cancel() }
    }

    /// The binding of an uncapped fold, so that opening it first grows the window to the open card, synchronously,
    /// then lets the caller's `withAnimation` run the fold. Closing passes through unchanged.
    public func preparing(_ binding: Binding<Bool>) -> Binding<Bool> {
        Binding(get: { binding.wrappedValue },
                set: { [weak self] newValue in
                    if newValue, !binding.wrappedValue { MainActor.assumeIsolated { self?.prepareOpening() } }
                    binding.wrappedValue = newValue
                })
    }

    public func prepareOpening() {
        guard let m = latest, m.fold.natural > 0 else { return }
        cancelSettle?(); cancelSettle = nil
        apply(PanelSizePolicy.preOpen(m))
    }

    public func report(_ m: PanelMeasure) {
        latest = m
        let d = PanelSizePolicy.decide(m, windowHeight: windowHeight())
        if let delay = d.settleAfter {
            if cancelSettle == nil {   // the timer runs from the close's start, later frames do not extend it
                cancelSettle = schedule(delay) { [weak self] in
                    guard let self else { return }
                    self.cancelSettle = nil
                    if let l = self.latest { self.apply(l.size) }
                }
            }
            apply(d.size)
        } else if cancelSettle != nil, m.fold.target == m.fold.current {
            return                      // closed but the hold is not over: the timer applies the latest measure
        } else {
            cancelSettle?(); cancelSettle = nil   // an opening, or any other change, ends the hold
            apply(d.size)
        }
    }

    /// The window is gone: forget the measure and any pending shrink.
    public func reset() { cancelSettle?(); cancelSettle = nil; latest = nil }
}
