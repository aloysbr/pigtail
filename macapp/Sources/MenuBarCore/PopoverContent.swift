import SwiftUI

/// The popover's sections, top to bottom.
public enum PopoverSection: Hashable, Sendable {
    case header, loading, daemonMessage, verdict, nextSession, detail, settings, footer
}

/// The whole popover, without its card: header, verdict, next session, per-account detail, settings,
/// its footer. `PopoverView` (app target) wraps it in the card and the size reporting.
public struct PopoverContent: View {
    public static let width: CGFloat = 320
    public static let loadingText = "Lecture des relevés…"
    /// The footer: opens the main window, quits the whole app (window and menu bar).
    public static let openWindowTitle = "Ouvrir Pigtail"
    public static let quitTitle = "Quitter Pigtail"

    let state: PopoverState
    let loaded: Bool
    let usage: UsageDisplay
    let lid: LidDisplay
    let lidNotificationsDenied: Bool
    let detailExpanded: Binding<Bool>
    let loginEnabled: Binding<Bool>
    let loginNeedsApproval: Bool
    let stopAll: () -> Void
    let quit: () -> Void
    let onLidAction: (LidAction) -> Void
    let now: Date
    let timeZone: TimeZone
    let captureStatic: Bool
    let openWindow: () -> Void

    public init(state: PopoverState, loaded: Bool, usage: UsageDisplay, lid: LidDisplay, lidNotificationsDenied: Bool,
                detailExpanded: Binding<Bool>, loginEnabled: Binding<Bool>, loginNeedsApproval: Bool,
                stopAll: @escaping () -> Void, quit: @escaping () -> Void, onLidAction: @escaping (LidAction) -> Void,
                now: Date = Date(), timeZone: TimeZone = .current, captureStatic: Bool = false,
                openWindow: @escaping () -> Void = {}) {
        self.state = state; self.loaded = loaded; self.usage = usage; self.lid = lid
        self.lidNotificationsDenied = lidNotificationsDenied
        self.detailExpanded = detailExpanded; self.loginEnabled = loginEnabled
        self.loginNeedsApproval = loginNeedsApproval
        self.stopAll = stopAll; self.quit = quit; self.onLidAction = onLidAction
        self.now = now; self.timeZone = timeZone; self.captureStatic = captureStatic
        self.openWindow = openWindow
    }

    // MARK: pure decisions

    /// Which sections show, in order. Never refreshed (`loaded` false) wins over `daemonDown`: the store
    /// starts at `daemonDown` (G9). A usage read that did not land keeps the header's metric (G12).
    public static func sections(state: PopoverState, loaded: Bool, usage: UsageDisplay) -> [PopoverSection] {
        if !loaded { return [.header, .loading, .settings, .footer] }
        if state.daemonDown { return [.header, .daemonMessage, .settings, .footer] }
        if usage.state == .ready { return [.header, .verdict, .nextSession, .detail, .settings, .footer] }
        return [.header, .verdict, .settings, .footer]
    }

    /// « Tout arrêter » (G6): absent without a server, while loading and when the daemon is down.
    public static func showsStopAll(state: PopoverState, loaded: Bool) -> Bool {
        loaded && !state.daemonDown && !state.runningRows.isEmpty
    }

    /// G7: « 6 actifs · 2,1 Go » — a decimal comma and a non-breaking space; `formatRSS` itself is untouched.
    public static func headerMetric(for state: PopoverState) -> String {
        var s = "\(state.activeCount) \(state.activeCount > 1 ? "actifs" : "actif")"   // 0 is singular in French
        if !state.totalRSSLabel.isEmpty {
            s += " · " + state.totalRSSLabel.replacingOccurrences(of: ".", with: ",").replacingOccurrences(of: " ", with: FrenchDuration.nb)
        }
        return s
    }

    var settingsModel: SettingsModel {
        SettingsModel(lid: lid, daemonDown: loaded && state.daemonDown, lidNotificationsDenied: lidNotificationsDenied,
                      loginEnabled: loginEnabled.wrappedValue, loginNeedsApproval: loginNeedsApproval,
                      now: now, timeZone: timeZone)
    }

    // MARK: drawing

    public var body: some View {
        VStack(spacing: 0) {
            ForEach(Self.sections(state: state, loaded: loaded, usage: usage), id: \.self) { section in
                if section != .header, section != .nextSession { Divider() }
                view(for: section)
            }
        }
        // Structurally 320 pt: the panel is sized on the hosting view's fittingSize, so a long line
        // must wrap inside this width, never widen it.
        .frame(width: Self.width)
    }

    @ViewBuilder private func view(for section: PopoverSection) -> some View {
        switch section {
        case .header: header
        case .loading:
            Text(Self.loadingText).font(.callout).foregroundStyle(BarPalette.textSecondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12).padding(.vertical, 10)
        case .daemonMessage:
            VerdictLine(model: VerdictModel(display: UsageDisplay(state: .unreachable, overview: nil, readFailed: true),
                                            now: now, timeZone: timeZone))
        case .verdict:
            VerdictLine(model: VerdictModel(display: usage, now: now, timeZone: timeZone))
        case .nextSession:
            if let o = usage.overview { NextSessionRow(model: NextSessionModel(overview: o, now: now)) }
        case .detail:
            if let o = usage.overview {
                AccountDetailSection(overview: o, now: now, expanded: detailExpanded, captureStatic: captureStatic)
            }
        case .settings:
            SettingsSectionView(model: settingsModel, login: loginEnabled, onLidAction: onLidAction)
        case .footer:
            HStack {
                Button(Self.openWindowTitle, action: openWindow).buttonStyle(.plain)
                Spacer()
                Button(Self.quitTitle, action: quit).buttonStyle(.plain)
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
        }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Text("🐷 pigtail").fontWeight(.semibold)
            Spacer(minLength: 4)
            if loaded, !state.daemonDown {
                Text(Self.headerMetric(for: state)).font(.callout).foregroundStyle(BarPalette.textSecondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if Self.showsStopAll(state: state, loaded: loaded) {
                Button("Tout arrêter", action: stopAll).buttonStyle(.plain).font(.callout).foregroundStyle(BarPalette.red)
            }
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
    }
}
