import AppKit
import WindowCore
import WebKit

/// Makes the main window's appearance follow the web front's Clair/Sombre/Système choice
/// (`window.appearance`: the window and its title bar; the menu bar popover follows the Mac).
/// Système (and before any message) leaves it unforced: the Mac decides. The decisions
/// and the WebKit path live in Core (`WindowAppearance`, `AppearanceInstaller`);
/// this is the AppKit side.
@MainActor
final class AppearanceBridge {
    /// The window the choice applies to, set by the coordinator when it creates it.
    var window: NSWindow? {
        get { target.window }
        set { target.window = newValue; applySaved() }
    }

    private let appearance: WindowAppearance
    private let installer: AppearanceInstaller
    private let target = WindowTarget()

    init(appearance: WindowAppearance = WindowAppearance(store: WindowAppearanceStore())) {
        self.appearance = appearance
        let target = self.target
        installer = AppearanceInstaller(appearance: appearance) { target.apply($0) }
    }

    /// Before any page: the last choice the web front made.
    func applySaved() {
        if let saved = appearance.current { target.apply(saved) }
    }

    func install(on controller: WKUserContentController) {
        installer.install(on: controller)
    }
}

/// Holds the window weakly, so the installer's closure can reach it without the bridge.
@MainActor
private final class WindowTarget {
    weak var window: NSWindow?

    func apply(_ appearance: AppearanceChoice) {
        // nil (Système): no forced appearance, the window follows the Mac.
        window?.appearance = appearance.appKitName.flatMap { NSAppearance(named: NSAppearance.Name($0)) }
    }
}
