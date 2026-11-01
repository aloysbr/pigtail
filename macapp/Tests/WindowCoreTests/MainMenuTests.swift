import AppKit
import XCTest
@testable import WindowCore

@MainActor
final class MainMenuTests: XCTestCase {
    @objc private func reloadNow(_ sender: Any?) {}

    private func menu() -> NSMenu {
        MainMenu.build(reloadTarget: self, reloadAction: #selector(reloadNow(_:)))
    }

    private func entry(_ m: NSMenu, _ title: String, file: StaticString = #filePath, line: UInt = #line) -> NSMenuItem? {
        let i = m.items.first { $0.title == title }
        XCTAssertNotNil(i, "missing \(title)", file: file, line: line)
        return i
    }

    func testTopLevel() {
        XCTAssertEqual(menu().items.map(\.title), ["Pigtail", "Édition", "Présentation", "Fenêtre"])
    }

    func testAppMenu() throws {
        let app = try XCTUnwrap(menu().items[0].submenu)
        XCTAssertNotNil(entry(app, "À propos de Pigtail"))
        let hide = try XCTUnwrap(entry(app, "Masquer Pigtail"))
        XCTAssertEqual(hide.keyEquivalent, "h")
        XCTAssertEqual(hide.keyEquivalentModifierMask, .command)
        let others = try XCTUnwrap(entry(app, "Masquer les autres"))
        XCTAssertEqual(others.keyEquivalent, "h")
        XCTAssertEqual(others.keyEquivalentModifierMask, [.command, .option])
        XCTAssertNotNil(entry(app, "Tout afficher"))
        let quit = try XCTUnwrap(entry(app, "Quitter Pigtail"))
        XCTAssertEqual(quit.keyEquivalent, "q")
        XCTAssertEqual(quit.action, #selector(NSApplication.terminate(_:)))
    }

    func testEditMenu() throws {
        let edit = try XCTUnwrap(menu().items[1].submenu)
        let expected: [(String, String, NSEvent.ModifierFlags, String)] = [
            ("Annuler", "z", .command, "undo:"),
            ("Rétablir", "z", [.command, .shift], "redo:"),
            ("Couper", "x", .command, "cut:"),
            ("Copier", "c", .command, "copy:"),
            ("Coller", "v", .command, "paste:"),
            ("Tout sélectionner", "a", .command, "selectAll:"),
        ]
        for (title, key, mask, sel) in expected {
            let i = try XCTUnwrap(entry(edit, title))
            XCTAssertEqual(i.keyEquivalent, key, title)
            XCTAssertEqual(i.keyEquivalentModifierMask, mask, title)
            XCTAssertEqual(i.action, Selector((sel)), title)
            XCTAssertNil(i.target, title)
        }
    }

    func testViewReload() throws {
        let view = try XCTUnwrap(menu().items[2].submenu)
        let reload = try XCTUnwrap(entry(view, "Recharger"))
        XCTAssertEqual(reload.keyEquivalent, "r")
        XCTAssertEqual(reload.keyEquivalentModifierMask, .command)
        XCTAssertEqual(reload.action, #selector(reloadNow(_:)))
        XCTAssertTrue(reload.target === self)
    }

    func testWindowMenu() throws {
        let window = try XCTUnwrap(menu().items[3].submenu)
        let mini = try XCTUnwrap(entry(window, "Réduire"))
        XCTAssertEqual(mini.keyEquivalent, "m")
        let close = try XCTUnwrap(entry(window, "Fermer"))
        XCTAssertEqual(close.keyEquivalent, "w")
        XCTAssertEqual(close.action, #selector(NSWindow.performClose(_:)))
        XCTAssertEqual(close.keyEquivalentModifierMask, .command)
        XCTAssertNil(close.target)
    }

    func testWebShortcutsLeftFree() {
        var all: [NSMenuItem] = []
        func walk(_ m: NSMenu) { for i in m.items { all.append(i); if let s = i.submenu { walk(s) } } }
        walk(menu())
        for i in all where i.keyEquivalentModifierMask.contains(.command) {
            XCTAssertFalse(["k", "b"].contains(i.keyEquivalent.lowercased()), i.title)
        }
    }
}
