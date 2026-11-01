import SwiftUI

/// The Travel Mode texts (D13, D15, D19), declared once: the tooltip and the
/// section read them from here.
public enum LidFormat {
    /// "HH:mm" in the Mac's local time, two digits.
    public static func hhmm(_ date: Date, timeZone: TimeZone = .current) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "HH:mm"
        return f.string(from: date)
    }

    /// D13; nil when off or unknown — the tooltip and the state line read this one function.
    public static func activeLine(_ d: LidDisplay, timeZone: TimeZone = .current) -> String? {
        guard case .on(let deadline) = d.state else { return nil }
        if let deadline { return "Travel Mode jusqu'à \(hhmm(deadline, timeZone: timeZone))" }
        return "Travel Mode actif (sans minuteur)"
    }

    public static let offLine = "Travel Mode désactivé"
    /// D15.
    public static let notSetUp = "lancer pigtail lid setup dans un terminal"
    /// G15: the app's own word for the daemon (« démon »).
    static let unreachable = "Démon injoignable"
    static let unavailable = "Travel Mode indisponible sur ce daemon"
    static let readFailed = "Lecture échouée — état précédent affiché"
    static func failed(_ reason: String) -> String { "Échec : \(reason)" }
    /// Q2: a guard (battery, thermal, lid) cannot read — the daemon's guard_error.
    public static func guardDown(_ m: String) -> String { "Garde inopérante : \(m)" }
    /// Q2: one text for refused, never answered and alerts turned off.
    public static let notificationsDenied =
        "Notifications non autorisées — les coupures du Travel Mode ne s'afficheront pas (Réglages Système › Notifications)"
}

/// SF Symbols of the popover section (the menu bar shows text instead, D23) — system symbols, never a bundle asset.
public enum LidGlyph {
    /// Settled D21 (G1).
    public static let travel = "suitcase.fill"
    /// Settled D21: outline moon.
    public static let off = "moon"
}

/// Everything the Travel Mode section says, as plain values: tested here, drawn by LidSectionView.
public struct LidSectionModel: Equatable, Sendable {
    public struct Button: Equatable, Sendable, Identifiable {
        public let id: String
        public let title: String
        public let action: LidAction
    }

    public let isVisible: Bool
    /// "Travel Mode" (kept, D19).
    public let header: String
    /// LidGlyph.travel (on) | LidGlyph.off (off) | nil.
    public let symbol: String?
    public let stateLine: String?
    public let notices: [String]
    /// "Activer" (off) | "Prolonger" (on) | nil (D19).
    public let durationLabel: String?
    /// "1 h", "4 h", "8 h" inline (D20); [] without a read state.
    public let durationButtons: [Button]
    /// "Désactiver" (on only).
    public let deactivateButton: Button?
    public let buttonsEnabled: Bool

    static let hours = [1, 4, 8]

    public init(display d: LidDisplay, notificationsDenied: Bool = false, timeZone: TimeZone = .current) {
        var notices: [String] = []
        switch d.state {
        case .unreachable: notices.append(LidFormat.unreachable)
        case .failed(let m): notices.append(LidFormat.failed(m))
        case .unavailable: notices.append(LidFormat.unavailable)
        case .loading, .on, .off: break
        }
        switch d.actionError {
        case .notSetUp?: notices.append(LidFormat.notSetUp)
        case .failed(let m)?: notices.append(LidFormat.failed(m))
        case .unreachable?: notices.append(LidFormat.unreachable)
        case .unavailable?: notices.append(LidFormat.unavailable)
        case nil: break
        }
        switch d.state {
        case .on, .off: if d.readFailed { notices.append(LidFormat.readFailed) }
        default: break
        }
        if let e = d.lastError { notices.append(LidFormat.failed(e)) }
        if let g = d.guardError { notices.append(LidFormat.guardDown(g)) }
        if notificationsDenied { notices.append(LidFormat.notificationsDenied) }
        var seen = Set<String>()
        self.notices = notices.filter { seen.insert($0).inserted }

        self.isVisible = d.state != .loading
        self.header = "Travel Mode"
        self.buttonsEnabled = !d.isActing
        switch d.state {
        case .on:
            self.symbol = LidGlyph.travel
            self.stateLine = LidFormat.activeLine(d, timeZone: timeZone)
            self.durationLabel = "Prolonger"
            self.durationButtons = Self.hours.map { Button(id: "extend-\($0)", title: "\($0) h", action: .extend(hours: $0)) }
            self.deactivateButton = Button(id: "deactivate", title: "Désactiver", action: .deactivate)
        case .off:
            self.symbol = LidGlyph.off
            self.stateLine = LidFormat.offLine
            self.durationLabel = "Activer"
            self.durationButtons = Self.hours.map { Button(id: "activate-\($0)", title: "\($0) h", action: .activate(hours: $0)) }
            self.deactivateButton = nil
        case .loading, .unreachable, .failed, .unavailable:
            self.symbol = nil
            self.stateLine = nil
            self.durationLabel = nil
            self.durationButtons = []
            self.deactivateButton = nil
        }
    }
}

/// Caption header, orange ⚠ notices, a
/// state line, then the actions inline (D20: buttons, never a Menu).
public struct LidSectionView: View {
    let model: LidSectionModel
    let onAction: (LidAction) -> Void

    /// Width of the state line's symbol slot, common to `suitcase.fill` (16 pt at `.callout`) and `moon`
    /// (14 pt): without it the text starts 2 pt further left in the off state than in the on one.
    /// `LidSectionTests.testSymbolSlotFitsBothSymbols` measures both symbols against it.
    static let symbolWidth: CGFloat = 16

    public init(model: LidSectionModel, onAction: @escaping (LidAction) -> Void) {
        self.model = model
        self.onAction = onAction
    }

    public var body: some View {
        if model.isVisible {
            VStack(alignment: .leading, spacing: 4) {
                Text(model.header)   // d8: free
                    .font(.caption)
                    .foregroundStyle(.secondary)
                ForEach(model.notices, id: \.self) { n in
                    HStack(alignment: .top, spacing: 4) {
                        Text("⚠")   // d8: free
                        LidLine(n, lines: LidLine.noticeLines).fixedSize(horizontal: false, vertical: true)
                    }
                    .font(.caption)
                    .foregroundStyle(.orange)
                }
                if let line = model.stateLine {
                    HStack(spacing: 6) {
                        if let symbol = model.symbol {
                            Image(systemName: symbol)
                                .frame(width: Self.symbolWidth, alignment: .center)
                                .accessibilityHidden(true)
                        }
                        LidLine(line, lines: LidLine.titleLines)
                    }
                    .font(.callout)
                }
                if let label = model.durationLabel {
                    HStack(spacing: 8) {
                        Text(label).font(.caption).foregroundStyle(.secondary)   // d8: free
                        ForEach(model.durationButtons) { b in button(b) }
                    }
                }
                if let deactivate = model.deactivateButton {
                    button(deactivate)
                }
            }
            .padding(.horizontal, 12)
            .padding(.top, 8)
            .frame(maxWidth: .infinity, alignment: .leading)   // no ideal/fixed width: the popover sets it
        }
    }

    private func button(_ b: LidSectionModel.Button) -> some View {
        SwiftUI.Button(b.title) { onAction(b.action) }
            .buttonStyle(.borderless)
            .controlSize(.small)
            .disabled(!model.buttonsEnabled)
    }
}

/// The only bounded Text of the section: a Text with no limit wraps an unbreakable word into a
/// tall narrow column, so every one-line text goes through here (moved from the Gates section,
/// the same limits: 3 lines for a notice, 2 for the state line, then « … »).
struct LidLine: View {
    static let noticeLines = 3
    static let titleLines = 2
    let text: String
    let lines: Int
    init(_ text: String, lines: Int) { self.text = text; self.lines = lines }
    var body: some View {
        Text(text).lineLimit(lines).truncationMode(.tail)
    }
}
