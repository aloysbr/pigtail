import JavaScriptCore
import XCTest
@testable import WindowCore

final class ThemeBridgeScriptTests: XCTestCase {
    /// A page stub, then the bridge script at document end.
    private func page(palette: String?, handler: Bool = true) -> JSContext {
        let ctx = JSContext()!
        ctx.evaluateScript("""
        var window = this; var posts = []; var observers = [];
        var document = { documentElement: { dataset: {} } };
        function MutationObserver(cb) { this.observe = function (target, opts) { observers.push({ cb: cb, target: target, opts: opts }); }; }
        """)
        if handler {
            ctx.evaluateScript("var webkit = { messageHandlers: { \(ThemeBridgeScript.handlerName): { postMessage: function (m) { posts.push(m); } } } };")
        }
        if let palette { ctx.evaluateScript("document.documentElement.dataset.themePalette = '\(palette)';") }
        ctx.evaluateScript(ThemeBridgeScript.source)
        XCTAssertNil(ctx.exception, "the script threw: \(ctx.exception?.toString() ?? "")")
        return ctx
    }

    private func posts(_ ctx: JSContext) -> [String] {
        ctx.evaluateScript("posts").toArray() as? [String] ?? []
    }

    /// The front sets the attribute and the observer fires.
    private func set(_ ctx: JSContext, _ palette: String) {
        ctx.evaluateScript("document.documentElement.dataset.themePalette = '\(palette)'; observers.forEach(function (o) { o.cb([]); });")
    }

    func testPostsThePaletteAtDocumentEnd() {
        XCTAssertEqual(posts(page(palette: "terra")), ["terra"])
    }

    func testFollowsAChangeOfPalette() {
        let ctx = page(palette: "terra")
        set(ctx, "ocean")
        XCTAssertEqual(posts(ctx), ["terra", "ocean"])
    }

    func testTheSameValueTwicePostsOnce() {
        let ctx = page(palette: "ocean")
        set(ctx, "ocean")
        XCTAssertEqual(posts(ctx), ["ocean"])
    }

    func testObservesOnlyThePaletteAttributeOfTheRoot() {
        let ctx = page(palette: "terra")
        XCTAssertEqual(ctx.evaluateScript("observers.length").toInt32(), 1)
        XCTAssertTrue(ctx.evaluateScript("observers[0].target === document.documentElement").toBool())
        XCTAssertEqual(ctx.evaluateScript("JSON.stringify(observers[0].opts)").toString(),
                       #"{"attributes":true,"attributeFilter":["data-theme-palette"]}"#)
    }

    /// The native daemon-down page (loadHTMLString) carries no palette.
    func testAPageWithoutThePaletteSendsNothing() {
        XCTAssertEqual(posts(page(palette: nil)), [])
    }

    func testNoHandlerNoError() {
        _ = page(palette: "terra", handler: false)
    }

    /// The contract the bridge reads, in the front's own sources: the root's
    /// data-theme-palette attribute, valued terra or ocean.
    func testTheFrontStillSetsTheAttribute() throws {
        let web = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("web/src/theme")
        let boot = try String(contentsOf: web.appendingPathComponent("boot.js"), encoding: .utf8)
        let appearance = try String(contentsOf: web.appendingPathComponent("appearance.ts"), encoding: .utf8)
        XCTAssertTrue(boot.contains("root.setAttribute('data-theme-palette', palette)"))
        XCTAssertTrue(appearance.contains("root.setAttribute('data-theme-palette', c.palette)"))
        XCTAssertTrue(appearance.contains("export type PaletteName = 'terra' | 'ocean'"))
        XCTAssertEqual(Set(ThemePalette.allCases.map(\.rawValue)), ["terra", "ocean"])
    }
}
