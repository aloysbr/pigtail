import Foundation

/// The web front's palette, as the web front sets it on `<html data-theme-palette>`
/// (web/src/theme/boot.js:24, appearance.ts applyChoice).
public enum ThemePalette: String, Sendable, CaseIterable {
    case ocean, terra

    /// The .icns in Contents/Resources the Dock shows, or nil for the
    /// bundle's own icon (AppIcon.icns, Océan).
    public var iconResourceName: String? { self == .terra ? "AppIconTerra" : nil }

    /// A message of the bridge: exactly "ocean" or "terra". Any other value,
    /// of any type, is ignored.
    public init?(message body: Any) {
        guard let s = body as? String, let palette = ThemePalette(rawValue: s) else { return nil }
        self = palette
    }
}

/// The last palette the web front showed, kept between launches (UserDefaults),
/// so the Dock shows it before the page loads.
public final class DockIconStore: @unchecked Sendable {
    static let key = "dockPalette"
    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// The saved palette; Océan (the bundle's icon) before any, or for an unreadable value.
    public func load() -> ThemePalette {
        defaults.string(forKey: Self.key).flatMap(ThemePalette.init(rawValue:)) ?? .ocean
    }

    public func save(_ palette: ThemePalette) {
        defaults.set(palette.rawValue, forKey: Self.key)
    }
}

/// Which palette the Dock shows, fed by the bridge's messages.
@MainActor
public final class DockIconTheme {
    public private(set) var current: ThemePalette
    private let store: DockIconStore

    public init(store: DockIconStore) {
        self.store = store
        current = store.load()
    }

    /// One bridge message. Returns the palette to apply when it changed, and
    /// saves it; nil for an unknown value or the palette already shown (the
    /// front's applyChoice is idempotent, the observer fires anyway).
    public func receive(_ body: Any) -> ThemePalette? {
        guard let palette = ThemePalette(message: body), palette != current else { return nil }
        current = palette
        store.save(palette)
        return palette
    }
}
