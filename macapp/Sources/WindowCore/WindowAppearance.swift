import Foundation

/// The user's choice in the web front (web/src/theme/appearance.ts: Clair,
/// Sombre, Système), not the resolved theme: Système resolves through the web
/// view's own prefers-color-scheme, which our forced appearance would drive.
public enum AppearanceChoice: String, Sendable, CaseIterable {
    case light, dark, system

    /// The NSAppearance.Name raw value the app sets app-wide: Aqua for light,
    /// Dark Aqua for dark (kept as a string so Core stays free of AppKit).
    /// nil for system: no forced appearance, the window follows the Mac.
    public var appKitName: String? {
        switch self {
        case .light: "NSAppearanceNameAqua"
        case .dark: "NSAppearanceNameDarkAqua"
        case .system: nil
        }
    }

    /// A message of the bridge: exactly "light", "dark" or "system". Any other value,
    /// of any type, is ignored.
    public init?(message body: Any) {
        guard let s = body as? String, let appearance = AppearanceChoice(rawValue: s) else { return nil }
        self = appearance
    }
}

/// The last choice the web front made, kept between launches (UserDefaults),
/// so the window has it before the page loads.
public final class WindowAppearanceStore: @unchecked Sendable {
    static let key = "windowAppearance"
    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// The saved choice; nil before any (the window follows the system), or for an unreadable value.
    public func load() -> AppearanceChoice? {
        defaults.string(forKey: Self.key).flatMap(AppearanceChoice.init(rawValue:))
    }

    public func save(_ appearance: AppearanceChoice) {
        defaults.set(appearance.rawValue, forKey: Self.key)
    }
}

/// Which appearance the window follows, fed by the bridge's messages.
@MainActor
public final class WindowAppearance {
    public private(set) var current: AppearanceChoice?
    private let store: WindowAppearanceStore

    public init(store: WindowAppearanceStore) {
        self.store = store
        current = store.load()
    }

    /// One bridge message. Returns the appearance to apply when it changed,
    /// and saves it; nil for an unknown value or the appearance already shown
    /// (the observer fires on every write of the attribute).
    public func receive(_ body: Any) -> AppearanceChoice? {
        guard let appearance = AppearanceChoice(message: body), appearance != current else { return nil }
        current = appearance
        store.save(appearance)
        return appearance
    }
}
