import AppCore
import AppKit

/// The app's menu bar with macOS's French labels (D14): app, Édition,
/// Présentation, Fenêtre. Nothing takes ⌘K or ⌘B: the web front owns them
/// (web/src/app/Shell.tsx).
@MainActor
public enum MainMenu {
    public static let editTitle = "Édition"
    public static let viewTitle = "Présentation"
    public static let windowTitle = "Fenêtre"
    public static let reloadTitle = "Recharger"

    public static func build(reloadTarget: AnyObject?, reloadAction: Selector) -> NSMenu {
        let name = AppIdentity.appName
        let main = NSMenu(title: "MainMenu")

        let app = NSMenu(title: name)
        app.addItem(item("À propos de \(name)", #selector(NSApplication.orderFrontStandardAboutPanel(_:)), ""))
        app.addItem(.separator())
        app.addItem(item("Masquer \(name)", #selector(NSApplication.hide(_:)), "h"))
        app.addItem(item("Masquer les autres", #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]))
        app.addItem(item("Tout afficher", #selector(NSApplication.unhideAllApplications(_:)), ""))
        app.addItem(.separator())
        app.addItem(item("Quitter \(name)", #selector(NSApplication.terminate(_:)), "q"))
        add(app, to: main)

        let edit = NSMenu(title: editTitle)
        edit.addItem(item("Annuler", Selector(("undo:")), "z"))
        edit.addItem(item("Rétablir", Selector(("redo:")), "z", [.command, .shift]))
        edit.addItem(.separator())
        edit.addItem(item("Couper", #selector(NSText.cut(_:)), "x"))
        edit.addItem(item("Copier", #selector(NSText.copy(_:)), "c"))
        edit.addItem(item("Coller", #selector(NSText.paste(_:)), "v"))
        edit.addItem(item("Tout sélectionner", #selector(NSText.selectAll(_:)), "a"))
        add(edit, to: main)

        let view = NSMenu(title: viewTitle)
        let reload = item(reloadTitle, reloadAction, "r")
        reload.target = reloadTarget
        view.addItem(reload)
        add(view, to: main)

        let window = NSMenu(title: windowTitle)
        window.addItem(item("Réduire", #selector(NSWindow.performMiniaturize(_:)), "m"))
        window.addItem(item("Fermer", #selector(NSWindow.performClose(_:)), "w"))
        add(window, to: main)

        return main
    }

    private static func item(_ title: String, _ action: Selector, _ key: String,
                             _ mask: NSEvent.ModifierFlags = .command) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: action, keyEquivalent: key)
        i.keyEquivalentModifierMask = mask
        i.target = nil // the responder chain
        return i
    }

    private static func add(_ submenu: NSMenu, to main: NSMenu) {
        let top = NSMenuItem(title: submenu.title, action: nil, keyEquivalent: "")
        top.submenu = submenu
        main.addItem(top)
    }
}
