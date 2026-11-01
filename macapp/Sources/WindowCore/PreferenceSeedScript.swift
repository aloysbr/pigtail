/// Seeds the web front's storage with the native palette and appearance, at
/// document start in the main frame, so a fresh web view does not start on the
/// web's defaults (and then overwrite the native choice through the bridges).
/// Writes a key only when it is absent: the web front's own choice always wins.
/// Goes through `window.localStorage` (the same object in WKWebView), never a
/// bare name, inside a try/catch: a blocked storage never throws.
public enum PreferenceSeedScript {
    public static let paletteKey = "pigtail-palette"
    public static let appearanceKey = "pigtail-appearance"

    /// nil when there is nothing to seed.
    public static func source(palette: ThemePalette?, appearance: AppearanceChoice?) -> String? {
        var seeds: [String] = []
        if let palette { seeds.append(seed(paletteKey, palette.rawValue)) }
        if let appearance { seeds.append(seed(appearanceKey, appearance.rawValue)) }
        guard !seeds.isEmpty else { return nil }
        return """
        (function () {
          try {
            var storage = window.localStorage;
        \(seeds.joined(separator: "\n"))
          } catch (e) {}
        })();
        """
    }

    // Keys and values are fixed identifiers and enum raw values: no quoting needed.
    private static func seed(_ key: String, _ value: String) -> String {
        "    if (storage.getItem('\(key)') === null) storage.setItem('\(key)', '\(value)');"
    }
}
