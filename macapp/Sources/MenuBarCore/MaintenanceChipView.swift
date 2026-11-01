import AppKit
import SwiftUI

/// The chip of `D4-B.html`: two lines, a progress bar, « Reprendre » on the right of line 1. One dark
/// look whatever the system theme (it floats over any desktop). States are words first (R9): the dot
/// only doubles the text. Colours: green / amber / blue from the mockup, system red for G5.
public struct MaintenanceChipView: View {
    public let display: MaintenanceChipDisplay
    public let isTakingBack: Bool
    /// The chip's one width, whatever the state: « Reprendre » never moves. The panel caps it to the screen.
    public let width: CGFloat
    public let onTakeBack: () -> Void
    /// Forces the hover look (verify captures only: a bitmap has no pointer).
    let previewHover: Bool
    @State private var hovered = false
    @State private var cursorPushed = false

    public static let defaultWidth: CGFloat = 760

    /// « Reprise… »: ONE dimming — dark text on a light grey fill — never `.disabled` on top of a faded fill.
    static let idleButtonFill = Color.white
    /// Pointer over « Reprendre »: the white fill dims to a light grey (1.25:1 against rest, label still ≥ 10:1).
    static let hoverButtonFill = Color(white: 0.90)
    static let hoverButtonContrast: Double = {
        func lum(_ w: Double) -> Double { w <= 0.03928 ? w / 12.92 : pow((w + 0.055) / 1.055, 2.4) }
        return (lum(1.0) + 0.05) / (lum(0.90) + 0.05)
    }()
    static let takingBackText = Color(red: 0x11 / 255, green: 0x11 / 255, blue: 0x11 / 255)
    static let takingBackFill = Color(red: 0xc8 / 255, green: 0xc8 / 255, blue: 0xcc / 255)
    /// WCAG contrast of the « Reprise… » label on its fill.
    static var takingBackButtonContrast: Double {
        func lum(_ r: Double, _ g: Double, _ b: Double) -> Double {
            func c(_ v: Double) -> Double { v <= 0.03928 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4) }
            return 0.2126 * c(r) + 0.7152 * c(g) + 0.0722 * c(b)
        }
        let t = lum(0x11 / 255, 0x11 / 255, 0x11 / 255), f = lum(0xc8 / 255, 0xc8 / 255, 0xcc / 255)
        return (max(t, f) + 0.05) / (min(t, f) + 0.05)
    }

    public init(display: MaintenanceChipDisplay, isTakingBack: Bool, width: CGFloat = MaintenanceChipView.defaultWidth,
                previewHover: Bool = false, onTakeBack: @escaping () -> Void) {
        self.previewHover = previewHover
        self.display = display
        self.isTakingBack = isTakingBack
        self.width = width
        self.onTakeBack = onTakeBack
    }

    static func color(_ d: MaintenanceChipDisplay.Dot) -> Color {
        switch d {
        case .green: return Color(red: 0x32 / 255, green: 0xd7 / 255, blue: 0x4b / 255)
        case .amber: return Color(red: 0xff / 255, green: 0xd6 / 255, blue: 0x0a / 255)
        case .blue: return Color(red: 0x64 / 255, green: 0xd2 / 255, blue: 0xff / 255)
        case .red: return Color(red: 0xff / 255, green: 0x45 / 255, blue: 0x3a / 255)
        }
    }

    public var body: some View {
        if let dot = display.dot {
            VStack(alignment: .leading, spacing: 0) {
                firstLine(dot: dot)
                switch display {
                case .running(let c), .passage(let c), .done(let c):
                    Text(c.queue)
                        .font(.system(size: 13.5))
                        .foregroundColor(.white.opacity(0.8))
                        .lineLimit(1).truncationMode(.tail)
                        .padding(.leading, 40).padding(.trailing, 20).padding(.bottom, 10)
                    progress(c.progress, done: dot == .blue)
                    ForEach(c.warnings, id: \.self) { warning($0) }
                case .failed(_, let served, let warnings):
                    if let served {
                        Text(served)
                            .font(.system(size: 13.5))
                            .foregroundColor(.white.opacity(0.8))
                            .lineLimit(1).truncationMode(.tail)
                            .padding(.leading, 40).padding(.trailing, 20).padding(.bottom, 10)
                    }
                    ForEach(warnings, id: \.self) { warning($0) }
                case .hidden:
                    EmptyView()
                }
            }
            .frame(width: width, alignment: .leading)
            .fixedSize(horizontal: false, vertical: true)
            .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(Color(red: 30 / 255, green: 30 / 255, blue: 32 / 255).opacity(0.82)))
            .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(Color.white.opacity(0.18), lineWidth: 0.5))
            .environment(\.colorScheme, .dark)
        }
    }

    @ViewBuilder private func firstLine(dot: MaintenanceChipDisplay.Dot) -> some View {
        HStack(spacing: 12) {
            // D4-B's `box-shadow: 0 0 0 4px` at .25: an 18 pt disc behind the 10 pt dot (a background does not take layout room).
            Circle().fill(Self.color(dot)).frame(width: 10, height: 10)
                .background(Circle().fill(Self.color(dot).opacity(0.25)).frame(width: 18, height: 18))
                .accessibilityHidden(true)
            switch display {
            case .running(let c), .passage(let c), .done(let c):
                Text(c.title).font(.system(size: 15, weight: .semibold)).lineLimit(1).fixedSize().layoutPriority(2)
                Rectangle().fill(Color.white.opacity(0.25)).frame(width: 1, height: 18)
                if let lead = c.lead {
                    Text(lead).foregroundColor(.white.opacity(0.78)).lineLimit(1).truncationMode(.middle)
                }
                Text(c.label).lineLimit(1).truncationMode(.middle)
                Text(c.time).font(.system(size: 15, weight: .semibold)).monospacedDigit().lineLimit(1).fixedSize().layoutPriority(2)
            case .failed(let cause, _, _):
                // D4-B's structure: the title is bold, the cause is plain text.
                Text("Maintenance").font(.system(size: 15, weight: .semibold)).lineLimit(1).fixedSize().layoutPriority(2)
                Rectangle().fill(Color.white.opacity(0.25)).frame(width: 1, height: 18)
                Text(cause).lineLimit(1).truncationMode(.middle)
            case .hidden:
                EmptyView()
            }
            Spacer(minLength: 12)
            Button(action: { if !isTakingBack { onTakeBack() } }) {
                Text(isTakingBack ? "Reprise…" : "Reprendre")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundColor(isTakingBack ? Self.takingBackText : Color(white: 0.07))
                    .padding(.horizontal, 15).padding(.vertical, 7)
                    .background(Capsule().fill(buttonFill))
                    .animation(NSWorkspace.shared.accessibilityDisplayShouldReduceMotion ? nil : .easeOut(duration: 0.12), value: hovered)
            }
            .buttonStyle(.plain)
            .onHover { inside in
                hovered = inside && !isTakingBack
                if hovered && !cursorPushed { NSCursor.pointingHand.push(); cursorPushed = true }
                else if !hovered && cursorPushed { NSCursor.pop(); cursorPushed = false }
            }
            .fixedSize().layoutPriority(2)
            .accessibilityLabel(isTakingBack ? "Reprise en cours" : "Reprendre le Mac")
        }
        .font(.system(size: 15))
        .foregroundColor(.white)
        .lineLimit(1)
        .padding(.leading, 18).padding(.trailing, 10).padding(.vertical, 9)
    }

    private var buttonFill: Color {
        if isTakingBack { return Self.takingBackFill }
        return hovered || previewHover ? Self.hoverButtonFill : Self.idleButtonFill
    }

    private func progress(_ p: Double, done: Bool) -> some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Color.white.opacity(0.2))
                Capsule().fill(Self.color(done ? .blue : .green)).frame(width: g.size.width * p)
            }
        }
        .frame(height: 4)
        .padding(.horizontal, 20).padding(.bottom, 12)
    }

    /// G5: a warning is a third line in amber text under the bar.
    private func warning(_ text: String) -> some View {
        Text(text)
            .font(.system(size: 13))
            .foregroundColor(Self.color(.amber))
            .padding(.leading, 40).padding(.trailing, 20).padding(.bottom, 10)
    }
}
