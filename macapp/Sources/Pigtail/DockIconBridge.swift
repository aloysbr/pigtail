import AppKit
import WindowCore
import WebKit

/// Puts the web front's palette on the Dock icon: Océan is the bundle's own
/// icon (`applicationIconImage = nil`), Terra is AppIconTerra.icns from
/// Contents/Resources. The decisions and the WebKit path live in Core
/// (`DockIconTheme`, `DockIconInstaller`); this is the AppKit side.
@MainActor
final class DockIconBridge {
    private let theme: DockIconTheme
    private let installer: DockIconInstaller

    init(theme: DockIconTheme = DockIconTheme(store: DockIconStore())) {
        self.theme = theme
        installer = DockIconInstaller(theme: theme) { Self.apply($0) }
    }

    /// At launch, before any page: the last palette the window showed.
    func applySaved() {
        Self.apply(theme.current)
    }

    func install(on controller: WKUserContentController) {
        installer.install(on: controller)
    }

    static func apply(_ palette: ThemePalette) {
        guard let name = palette.iconResourceName,
              let url = Bundle.main.url(forResource: name, withExtension: "icns"),
              let image = NSImage(contentsOf: url) else {
            // Océan, or Terra missing (a `swift run` outside the bundle): the bundle's own icon.
            NSApp.applicationIconImage = nil
            return
        }
        NSApp.applicationIconImage = image
    }
}
