import SwiftUI

/// What « Réglages » says, as plain values: tested here, drawn by `SettingsSectionView`.
public struct SettingsModel: Equatable, Sendable {
    public let header = "Réglages"
    public let travelTitle = "Travel Mode"
    public let travelOn: Bool
    /// « Désactivé » | « <dur> restantes · jusqu’à HH:mm » | « Actif, sans minuteur » (G8) |
    /// « Indisponible sans le démon »; nil while Travel Mode is not readable.
    public let travelSubtitle: String?
    public let travelEnabled: Bool
    /// « Prolonger » + « 1 h », « 4 h », « 8 h » while Travel Mode is on; [] otherwise.
    public let extendLabel: String?
    public let extendButtons: [LidSectionModel.Button]
    /// Orange legends under the Travel Mode row — #263's included (« Garde inopérante : … »,
    /// « Notifications non autorisées … »), G15's « Démon injoignable ».
    public let notices: [String]
    public let loginTitle = "Démarrer à la connexion"
    public let loginOn: Bool
    /// « Démarrer à la connexion » works without the daemon.
    public let loginRowEnabled = true
    public let loginCaption: String?

    /// D11: the switch turns Travel Mode on for 4 hours.
    public static let switchOnHours = 4

    public init(lid d: LidDisplay, daemonDown: Bool, lidNotificationsDenied: Bool, loginEnabled: Bool,
                loginNeedsApproval: Bool, now: Date, timeZone: TimeZone = .current) {
        let section = LidSectionModel(display: d, notificationsDenied: lidNotificationsDenied, timeZone: timeZone)
        // no « Démon injoignable » when the daemon is down altogether: the subtitle already says it
        notices = daemonDown ? section.notices.filter { $0 != LidFormat.unreachable } : section.notices
        let readable: Bool
        switch d.state {
        case .on, .off: readable = true
        default: readable = false
        }
        travelOn = d.isActive
        travelEnabled = !daemonDown && readable && !d.isActing
        if daemonDown {
            travelSubtitle = "Indisponible sans le démon"
        } else {
            switch d.state {
            case .on(let deadline?):
                let left = FrenchDuration.dur(minutes: FrenchDuration.minutes(from: now, to: deadline))
                travelSubtitle = "\(left) restantes · jusqu\(FrenchDuration.apostrophe)à \(LidFormat.hhmm(deadline, timeZone: timeZone))"
            case .on(nil): travelSubtitle = "Actif, sans minuteur"
            case .off: travelSubtitle = "Désactivé"
            default: travelSubtitle = nil
            }
        }
        if !daemonDown, d.isActive {
            extendLabel = "Prolonger"
            extendButtons = LidSectionModel.hours.map {
                LidSectionModel.Button(id: "extend-\($0)", title: "\($0) h", action: .extend(hours: $0))
            }
        } else {
            extendLabel = nil
            extendButtons = []
        }
        loginOn = loginEnabled
        loginCaption = loginNeedsApproval ? "À autoriser dans Réglages Système › Général › Ouverture au login" : nil
    }

    public func travelAction(turningOn: Bool) -> LidAction {
        turningOn ? .activate(hours: Self.switchOnHours) : .deactivate
    }
}

/// A switch, its title and its sub-title, read as one control.
public struct SettingRow: View {
    let title: String
    let subtitle: String?
    let isOn: Binding<Bool>
    let enabled: Bool

    public init(title: String, subtitle: String?, isOn: Binding<Bool>, enabled: Bool = true) {
        self.title = title; self.subtitle = subtitle; self.isOn = isOn; self.enabled = enabled
    }

    public static func accessibilityLabel(title: String) -> String { title }
    public static func accessibilityHint(subtitle: String?) -> String? { subtitle }

    public var body: some View {
        HStack(alignment: .center, spacing: 8) {
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.body)
                if let subtitle {
                    Text(subtitle).font(.caption).foregroundStyle(BarPalette.textSecondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .accessibilityHidden(true)   // read through the switch below: one control, not three elements
            Spacer(minLength: 4)
            Toggle(title, isOn: isOn)
                .labelsHidden()
                .toggleStyle(.switch)
                .controlSize(.small)
                .disabled(!enabled)
                .accessibilityLabel(Self.accessibilityLabel(title: title))
                .accessibilityHint(Self.accessibilityHint(subtitle: subtitle) ?? "")
        }
        .padding(.vertical, 4)
    }
}

public struct SettingsSectionView: View {
    let model: SettingsModel
    let onLidAction: (LidAction) -> Void
    let login: Binding<Bool>

    public init(model: SettingsModel, login: Binding<Bool>, onLidAction: @escaping (LidAction) -> Void) {
        self.model = model; self.login = login; self.onLidAction = onLidAction
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(model.header).font(.caption).foregroundStyle(BarPalette.textSecondary)
            SettingRow(title: model.travelTitle, subtitle: model.travelSubtitle,
                       isOn: Binding(get: { model.travelOn },
                                     set: { onLidAction(model.travelAction(turningOn: $0)) }),
                       enabled: model.travelEnabled)
            if let label = model.extendLabel {
                HStack(spacing: 8) {
                    Text(label).font(.caption).foregroundStyle(BarPalette.textSecondary)
                    ForEach(model.extendButtons) { b in
                        Button(b.title) { onLidAction(b.action) }.buttonStyle(.borderless).controlSize(.small)
                    }
                }
            }
            ForEach(model.notices, id: \.self) { n in
                Text(n).font(.caption).foregroundStyle(BarPalette.orangeText)
                    .fixedSize(horizontal: false, vertical: true)
            }
            SettingRow(title: model.loginTitle, subtitle: nil, isOn: login, enabled: model.loginRowEnabled)
            if let c = model.loginCaption {
                Text(c).font(.caption).foregroundStyle(BarPalette.textSecondary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
