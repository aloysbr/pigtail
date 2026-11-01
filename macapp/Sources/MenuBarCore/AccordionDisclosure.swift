import SwiftUI

/// What an AccordionDisclosure header says to accessibility about its state — the UI's own word
/// for it, « repli » (verify N2 r2).
public enum AccordionAccessibility {
    public static func value(expanded: Bool) -> String { expanded ? "déplié" : "replié" }
}

/// The one Animation of every fold: 0.22 s ease-in-out (I5), none under « Réduire les animations » (G10).
public enum AccordionMotion {
    public static let duration: Double = 0.22
    public static func animation(reduceMotion: Bool) -> Animation? {
        reduceMotion ? nil : .easeInOut(duration: duration)
    }
}

private struct UncappedHeightKey: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

/// The content at its natural height, 0 while closed (still measured, so reopening reveals the real
/// height), clipped and hidden from accessibility when closed. No ScrollView: nothing scrolls.
private struct UncappedFold<Content: View>: View {
    let expanded: Bool
    let content: Content
    @State private var measured: CGFloat = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 0) { content }
            .frame(height: expanded ? measured : 0, alignment: .top)
            .clipped()
            .accessibilityHidden(!expanded)   // clipped to 0 is still in the AX tree: hide it too
            .background(GeometryReader { proxy in
                // What the host window needs (PanelSizePolicy): the height on screen now, and where it settles.
                Color.clear.preference(key: PanelMeasureKey.self,
                                       value: PanelMeasure(size: .zero, fold: FoldHeights(current: proxy.size.height,
                                                                                         target: expanded ? measured : 0,
                                                                                         natural: measured)))
            })
            .background(alignment: .top) {
                // The measuring copy: never drawn, never read by VoiceOver, laid out at its ideal height.
                VStack(alignment: .leading, spacing: 0) { content }
                    .fixedSize(horizontal: false, vertical: true)
                    .hidden()
                    .accessibilityHidden(true)
                    .background(GeometryReader { proxy in
                        Color.clear.preference(key: UncappedHeightKey.self, value: proxy.size.height)
                    })
            }
            .onPreferenceChange(UncappedHeightKey.self) { measured = $0 }
    }
}

/// Custom disclosure with an animatable explicit height. Unlike SwiftUI's `DisclosureGroup` — which
/// reserves its full layout height instantly and only animates a content transition, making the
/// resized host panel jump (the reason this view exists) — this animates `frame(height:)` from 0
/// to the measured content height, capped at `maxContentHeight` (beyond it the inner ScrollView
/// scrolls). `scrollable: false` is for captures only: ImageRenderer cannot draw a ScrollView, so the
/// content is drawn directly, in full, when `expanded`.
public struct AccordionDisclosure<Label: View, Content: View>: View {
    private let maxContentHeight: CGFloat
    private let scrollable: Bool
    private let capped: Bool
    private let headerInsets: EdgeInsets
    private let collapsedBottomInset: CGFloat
    private let label: Label
    private let content: Content
    /// The state is the caller's (`expanded: Binding`) or the fold's own (`expanded: Bool`, its initial value).
    private let binding: Binding<Bool>?
    @State private var ownExpanded: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var expanded: Bool { binding?.wrappedValue ?? ownExpanded }

    private func toggle() {
        withAnimation(AccordionMotion.animation(reduceMotion: reduceMotion)) {
            if let binding { binding.wrappedValue.toggle() } else { ownExpanded.toggle() }
        }
    }

    /// `headerInsets` pads the header row only (chevron + label); the default (4 pt above and below,
    /// none at the sides) is the plain fold's. A caller whose
    /// title sits at other margins passes its own.
    /// `collapsedBottomInset` is the space under the closed fold, for a caller whose rows carried
    /// the space under the block; 0 by default.
    /// `capped: false` takes the uncapped animated branch and ignores `scrollable` and `maxContentHeight`.
    public init(expanded: Bool = false, capped: Bool = true, scrollable: Bool = true, maxContentHeight: CGFloat = 160,
                headerInsets: EdgeInsets = EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0),
                collapsedBottomInset: CGFloat = 0,
                @ViewBuilder label: () -> Label, @ViewBuilder content: () -> Content) {
        binding = nil
        _ownExpanded = State(initialValue: expanded)
        self.capped = capped
        self.scrollable = scrollable
        self.headerInsets = headerInsets
        self.collapsedBottomInset = collapsedBottomInset
        self.maxContentHeight = maxContentHeight
        self.label = label()
        self.content = content()
    }

    /// The same fold driven by the caller's state (the usage detail keeps it in `UsageMonitor`, I9).
    public init(expanded: Binding<Bool>, capped: Bool = true, scrollable: Bool = true, maxContentHeight: CGFloat = 160,
                headerInsets: EdgeInsets = EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0),
                collapsedBottomInset: CGFloat = 0,
                @ViewBuilder label: () -> Label, @ViewBuilder content: () -> Content) {
        binding = expanded
        _ownExpanded = State(initialValue: expanded.wrappedValue)
        self.capped = capped
        self.scrollable = scrollable
        self.headerInsets = headerInsets
        self.collapsedBottomInset = collapsedBottomInset
        self.maxContentHeight = maxContentHeight
        self.label = label()
        self.content = content()
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                toggle()
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(BarPalette.textSecondary)   // viewer.css:140, --fg2
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                    label
                    Spacer()
                }
                .padding(headerInsets)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(AccordionAccessibility.value(expanded: expanded))

            if !capped {
                UncappedFold(expanded: expanded, content: content)
            } else if scrollable {
                // Its height is the content's, capped, 0 while closed (CappedScrollView): never the
                // height the panel happens to propose.
                CappedScrollView(maxHeight: maxContentHeight, collapsed: !expanded) { content }
                .clipped()
                .accessibilityHidden(!expanded)   // clipped to 0 is still in the AX tree: hide it too
            } else if expanded {
                // Unconstrained, a two-line Text falls back to one line here: the vertical
                // fixedSize gives each row its full height, as the ScrollView branch does.
                VStack(alignment: .leading, spacing: 0) { content }
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        // Closed, the space under the block is the fold's own (`collapsedBottomInset`); open, the
        // content's last row carries it, so nothing is added. Animated with the toggle.
        .padding(.bottom, expanded ? 0 : collapsedBottomInset)
    }
}
