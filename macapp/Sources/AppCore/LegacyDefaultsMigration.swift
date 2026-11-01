import Foundation

/// Carries the preferences of the two retired apps into Pigtail, once.
///
/// From the old window app's domain: the HTTP address, the Dock palette, the
/// appearance and the window frame; from the old menu bar app's domain: the
/// status item's position, the cmux notice ledger and a login opt-out (only `false`: an opt-in must let
/// the fresh policy register the new bundle). A target key already set is never
/// overwritten, the legacy domains are never modified (a way back stays open),
/// and once `doneKey` is set the legacy domains are never read again.
///
/// Palette absent from a used legacy window domain means Océan: the old app read
/// a missing `cockpitDockPalette` as Océan and saved a palette only when the web
/// sent another one, whereas the new web front would start on Terra.
public enum LegacyDefaultsMigration {
    public static let doneKey = "legacyDefaultsMigrated"
    /// The old window app's bundle id, which is also its defaults domain.
    public static let legacyWindowDomain = "sh.pigtail.cockpit"
    /// The old menu bar app's bundle id, which is also its defaults domain.
    public static let legacyBarDomain = "sh.pigtail.bar"
    /// The apps Pigtail replaces, and terminates at launch from the release bundle.
    public static let retiredBundleIDs = [legacyBarDomain, legacyWindowDomain]

    static let paletteKey = "dockPalette"
    static let legacyPaletteKey = "cockpitDockPalette"
    /// Legacy window keys that prove the old window app was used.
    static let legacyWindowUseKeys = ["cockpitHTTPAddr", "cockpitWindowAppearance", "NSWindow Frame PigtailCockpitMainWindow"]
    static let windowKeys: [(legacy: String, target: String)] = [
        ("cockpitHTTPAddr", "httpAddr"),
        (legacyPaletteKey, paletteKey),
        ("cockpitWindowAppearance", "windowAppearance"),
        ("NSWindow Frame PigtailCockpitMainWindow", "NSWindow Frame PigtailMainWindow"),
    ]
    /// The status item has no autosaveName, in the old app as in Pigtail: it reads this key.
    static let statusItemPositionKey = "NSStatusItem Preferred Position Item-0"
    static let loginPreferenceKey = "LaunchAtLogin.userPreference"
    /// The cmux end-of-pass notice ledger (`DefaultsNoticeLedger`): carried so a
    /// notice the old bar already posted is not posted again.
    static let checkNoticeLedgerKey = "cmuxCheckNoticeIDs"

    /// Returns the target keys written (empty when already done or nothing to carry).
    /// Read `target.bool(forKey: doneKey)` before calling it to tell those two apart.
    @discardableResult
    public static func run(into target: UserDefaults, window legacyWindow: UserDefaults, bar legacyBar: UserDefaults) -> [String] {
        guard !target.bool(forKey: doneKey) else { return [] }
        var written: [String] = []
        func carry(_ value: Any, to key: String) {
            guard target.object(forKey: key) == nil else { return }
            target.set(value, forKey: key)
            written.append(key)
        }

        for (legacy, key) in windowKeys {
            if let value = legacyWindow.object(forKey: legacy) { carry(value, to: key) }
        }
        let windowUsed = legacyWindowUseKeys.contains { legacyWindow.object(forKey: $0) != nil }
        if windowUsed, legacyWindow.object(forKey: legacyPaletteKey) == nil {
            carry("ocean", to: paletteKey)
        }

        if let position = legacyBar.object(forKey: statusItemPositionKey) {
            carry(position, to: statusItemPositionKey)
        }
        if (legacyBar.object(forKey: loginPreferenceKey) as? Bool) == false {
            carry(false, to: loginPreferenceKey)
        }

        if let ledger = legacyBar.object(forKey: checkNoticeLedgerKey) {
            carry(ledger, to: checkNoticeLedgerKey)
        }

        target.set(true, forKey: doneKey)
        return written
    }
}
