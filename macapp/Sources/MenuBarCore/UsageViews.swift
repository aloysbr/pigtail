import SwiftUI

// The usage zone's views: every text comes from UsageModels.swift, every colour from BarPalette.
// Measures are the mockup's (`viewer.css:194-231`).

// MARK: - AttentionGlyph

/// A discreet triangle that asks for attention; its label says why.
public struct AttentionGlyph: View {
    let reason: String

    public init(reason: String) { self.reason = reason }

    public static func label(for reason: String) -> String { "attention : \(reason)" }

    public var body: some View {
        Image(systemName: "exclamationmark.triangle.fill")
            .font(.system(size: 9))
            .foregroundStyle(BarPalette.orangeFill)
            .accessibilityLabel(AttentionGlyph.label(for: reason))
    }
}

// MARK: - VerdictLine

/// The verdict: a dot, one sentence, its sub-line and at most one discreet note per cause.
public struct VerdictLine: View {
    let model: VerdictModel

    public init(model: VerdictModel) { self.model = model }

    private var dotColor: Color {
        switch model.tone {
        case .ok: return BarPalette.green
        case .warn: return BarPalette.orangeFill
        case .alarm: return BarPalette.red
        case .muted: return Color.secondary.opacity(0.5)
        }
    }
    private var phraseColor: Color {
        switch model.tone {
        case .ok: return .primary
        case .warn: return BarPalette.orangeText
        case .alarm: return BarPalette.red
        case .muted: return BarPalette.textSecondary
        }
    }

    public var body: some View {
        if model.isDaemonMessage {
            VStack(spacing: 2) {
                Text(model.phrase).font(.callout)
                if let sub = model.sub { Text(sub).font(.caption) }
            }
            .foregroundStyle(BarPalette.textSecondary)
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, 12)
            .padding(.top, 12)
            .padding(.bottom, 10)
        } else {
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 7) {
                    Circle().fill(dotColor).frame(width: 8, height: 8).accessibilityHidden(true)
                    Text(model.phrase)
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(phraseColor)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let sub = model.sub {
                    Text(sub).font(.callout)
                        .foregroundStyle(model.tone == .muted ? BarPalette.textSecondary : Color.primary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.leading, 15)
                }
                ForEach(Array(model.notes.enumerated()), id: \.offset) { _, note in
                    Text(note.text).font(.caption)
                        .foregroundStyle(note.orange ? BarPalette.orangeText : BarPalette.textSecondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.leading, 15)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 12)
            .padding(.top, 10)
            .padding(.bottom, 9)
            .accessibilityElement(children: .combine)
        }
    }
}

// MARK: - NextSessionRow

/// « Prochaine session » on the left, « perso **i** · gx **t** » on the right.
public struct NextSessionRow: View {
    let model: NextSessionModel

    public init(model: NextSessionModel) { self.model = model }

    public var body: some View {
        if !model.entries.isEmpty {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text("Prochaine session").font(.caption).foregroundStyle(BarPalette.textSecondary)
                Spacer(minLength: 6)
                HStack(spacing: 4) {
                    ForEach(Array(model.entries.enumerated()), id: \.offset) { idx, e in
                        if idx > 0 { Text("·").foregroundStyle(BarPalette.textSecondary).accessibilityHidden(true) }
                        HStack(spacing: 3) {
                            Text("\(e.display) ") + Text(e.letter).fontWeight(.semibold)
                            if let reason = e.attention { AttentionGlyph(reason: reason) }
                        }
                        .accessibilityElement(children: .combine)
                        .accessibilityLabel(e.accessibilityLabel)
                    }
                }
                .font(.callout)
                .fixedSize()
            }
            .padding(.horizontal, 12)
            .padding(.bottom, 9)
        }
    }
}

// MARK: - RhythmBar

/// The week's availability bar (3 pt) and the tick of the share of the window still to run.
public struct RhythmBar: View {
    let fraction: Double
    let tick: Double?
    let orange: Bool

    public init(fraction: Double, tick: Double?, orange: Bool) {
        self.fraction = fraction; self.tick = tick; self.orange = orange
    }

    public var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(BarPalette.track).frame(height: 3)
                Capsule().fill(orange ? BarPalette.orangeFill : BarPalette.fill)
                    .frame(width: max(0, min(1, fraction)) * geo.size.width, height: 3)
                if let tick {
                    RoundedRectangle(cornerRadius: 0.5).fill(Color.primary)
                        .frame(width: 1, height: 7)
                        .offset(x: max(0, min(1, tick)) * geo.size.width - 0.5)
                }
            }
            .frame(height: 7)
        }
        .frame(height: 7)
        .accessibilityHidden(true)
    }
}

// MARK: - AccountRow

public struct AccountRow: View {
    let model: AccountRowModel

    public init(model: AccountRowModel) { self.model = model }

    public var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            if let reason = model.unusableReason {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    letter.opacity(0.5)
                    Text(reason).font(.callout).foregroundStyle(BarPalette.red)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 0)
                }
            } else {
                HStack(alignment: .top, spacing: 6) {
                    letter
                    VStack(alignment: .leading, spacing: 3) {
                        (Text(model.weekLine).foregroundStyle(Color.primary)
                         + Text(model.fast ? " · rapide" : "").foregroundStyle(BarPalette.orangeText))
                            .font(.callout).monospacedDigit()
                            .fixedSize(horizontal: false, vertical: true)
                        RhythmBar(fraction: model.barFraction, tick: model.tickFraction, orange: model.barOrange)
                    }
                    // viewer.css:196, `14px 1fr auto` with a 6 px gap: the bar takes the width left, the 5 h
                    // column its content's width, 6 pt away; no spacer between them.
                    .frame(maxWidth: .infinity, alignment: .leading)
                    if let five = model.fiveHour {
                        fiveHourText(five)
                            .font(.callout).monospacedDigit()
                            .fixedSize(horizontal: false, vertical: true)
                            .layoutPriority(1)
                    }
                }
                VStack(alignment: .leading, spacing: 0) {
                    if let r = model.renewals { Text(r).foregroundStyle(BarPalette.textSecondary) }
                    if let n = model.notices { Text(n).foregroundStyle(BarPalette.orangeText) }
                }
                .font(.caption)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.leading, 20)
            }
        }
        .padding(.top, 5)
        .padding(.bottom, 4)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// `viewer.js:433`: « 5 h **84 %** dispo », the value bold and primary, the words secondary; a
    /// saturated window is one orange semibold phrase.
    private func fiveHourText(_ five: String) -> Text {
        if model.fiveHourOrange { return Text(five).fontWeight(.semibold).foregroundStyle(BarPalette.orangeText) }
        let words = five.components(separatedBy: " ")      // « 5 nb h », « 84 nb % », « dispo »
        guard words.count == 3, words[1].hasSuffix("%") else {
            return Text(five).foregroundStyle(BarPalette.textSecondary)
        }
        return Text(words[0] + " ").foregroundStyle(BarPalette.textSecondary)
            + Text(words[1]).fontWeight(.semibold).foregroundStyle(Color.primary)
            + Text(" " + words[2]).foregroundStyle(BarPalette.textSecondary)
    }

    private var letter: some View {
        Text(model.letter).font(.system(size: 13, weight: .semibold)).frame(width: 14, alignment: .leading)
    }
}

// MARK: - AccountDetailSection

/// What the detail section says, as plain values.
public struct AccountDetailModel: Equatable, Sendable {
    public let rows: [AccountRowModel]
    public let header: DetailHeaderModel

    public init(overview: UsageOverview, now: Date) {
        rows = overview.accounts.map { AccountRowModel(account: $0, now: now) }
        header = DetailHeaderModel(overview: overview, now: now)
    }

    /// G16: the orange legend heads the OPEN detail only.
    public func caption(expanded: Bool) -> String? { expanded ? header.unreadableCaption : nil }
    /// The dot hides once the detail is open (the rows say it themselves).
    public func dotVisible(expanded: Bool) -> Bool { header.showsDot && !expanded }
}

/// « Détail par compte »: folded by default, no height cap, 0.22 s ease-in-out. `captureStatic` is for
/// `UsageCaptureTests` only: `ImageRenderer` has no window nor layout passes, the measure of the
/// animated branch would never arrive; the captures-only branch draws the same rows.
public struct AccountDetailSection: View {
    let model: AccountDetailModel
    let expanded: Binding<Bool>
    let captureStatic: Bool

    public init(overview: UsageOverview, now: Date, expanded: Binding<Bool>, captureStatic: Bool = false) {
        model = AccountDetailModel(overview: overview, now: now)
        self.expanded = expanded
        self.captureStatic = captureStatic
    }

    private var label: some View {
        HStack(spacing: 6) {
            Text("Détail par compte").font(.callout).foregroundStyle(.primary)
            if model.dotVisible(expanded: expanded.wrappedValue) {
                Circle().fill(BarPalette.orangeFill).frame(width: 6, height: 6)
                    .accessibilityLabel(model.header.dotLabel)
            }
        }
    }

    private var rows: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let c = model.caption(expanded: true) {
                Text(c).font(.caption).foregroundStyle(BarPalette.orangeText)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.bottom, 3)
            }
            ForEach(Array(model.rows.enumerated()), id: \.offset) { idx, row in
                if idx > 0 { Divider() }
                AccountRow(model: row)
            }
        }
    }

    public var body: some View {
        Group {
            if captureStatic {
                AccordionDisclosure(expanded: expanded.wrappedValue, scrollable: false) { label } content: { rows }
            } else {
                AccordionDisclosure(expanded: expanded, capped: false) { label } content: { rows }
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 6)      // `.det { padding: 6px 12px 8px }` (viewer.css:188)
        .padding(.bottom, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
