import SwiftUI

private struct CappedScrollContentHeightKey: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

/// A ScrollView as tall as its content, up to `maxHeight` — then it scrolls.
///
/// Why not `ScrollView { … }.frame(maxHeight:)`: a ScrollView has no height of its own, it takes
/// the height it is proposed. In the popover that proposal is the panel's previous size (the panel
/// follows the size the content measures, AppDelegate.resizePanel), so the list kept whatever the
/// last size imposed: squeezed to 0 pt when a fold opened below it, stretched to its cap with a
/// void under its rows when the panel was large (verify N2 r3, PopoverFoldLayoutTests). Here the
/// height is a fact of the content: measured on a hidden copy laid out at its ideal height, outside
/// the ScrollView (a preference set inside a ScrollView's content did not reach the outside — the
/// former AccordionDisclosure fell back to its whole cap, measured 2026-10-08), then capped.
/// `collapsed` draws it at 0 pt (a closed fold), still measured, so opening reveals the real height.
public struct CappedScrollView<Content: View>: View {
    private let maxHeight: CGFloat
    private let collapsed: Bool
    private let content: Content
    @State private var contentHeight: CGFloat = 0

    public init(maxHeight: CGFloat, collapsed: Bool = false, @ViewBuilder content: () -> Content) {
        self.maxHeight = maxHeight
        self.collapsed = collapsed
        self.content = content()
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) { content }
        }
        .frame(height: collapsed ? 0 : min(contentHeight, maxHeight), alignment: .top)
        .background(alignment: .top) {
            // The measuring copy: never drawn, never read by VoiceOver, laid out at its ideal height.
            VStack(alignment: .leading, spacing: 0) { content }
                .fixedSize(horizontal: false, vertical: true)
                .hidden()
                .accessibilityHidden(true)
                .background(GeometryReader { proxy in
                    Color.clear.preference(key: CappedScrollContentHeightKey.self, value: proxy.size.height)
                })
        }
        .onPreferenceChange(CappedScrollContentHeightKey.self) { contentHeight = $0 }
    }
}
