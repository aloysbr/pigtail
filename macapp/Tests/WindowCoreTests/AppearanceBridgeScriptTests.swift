import JavaScriptCore
import XCTest
@testable import WindowCore

final class AppearanceBridgeScriptTests: XCTestCase {
    /// A page stub, then the bridge script at document end.
    /// `choice` is the localStorage `pigtail-appearance` value (nil: unset); `storage` false: access throws (blocked).
    private func page(theme: String?, choice: String? = nil, storage: Bool = true, handler: Bool = true) -> JSContext {
        let ctx = JSContext()!
        ctx.evaluateScript("""
        var window = this; var posts = []; var observers = [];
        var document = { documentElement: { dataset: {} } };
        function MutationObserver(cb) { this.observe = function (target, opts) { observers.push({ cb: cb, target: target, opts: opts }); }; }
        """)
        if storage {
            let value = choice.map { "'\($0)'" } ?? "null"
            ctx.evaluateScript("var store = { 'pigtail-appearance': \(value) }; var localStorage = { getItem: function (k) { var v = store[k]; return v === undefined ? null : v; } };")
        } else {
            ctx.evaluateScript("Object.defineProperty(this, 'localStorage', { get: function () { throw new Error('SecurityError'); } });")
        }
        if handler {
            ctx.evaluateScript("var webkit = { messageHandlers: { \(AppearanceBridgeScript.handlerName): { postMessage: function (m) { posts.push(m); } } } };")
        }
        if let theme { ctx.evaluateScript("document.documentElement.dataset.theme = '\(theme)';") }
        ctx.evaluateScript(AppearanceBridgeScript.source)
        XCTAssertNil(ctx.exception, "the script threw: \(ctx.exception?.toString() ?? "")")
        return ctx
    }

    private func posts(_ ctx: JSContext) -> [String] {
        ctx.evaluateScript("posts").toArray() as? [String] ?? []
    }

    /// The front saves the choice, then sets the attribute and the observer fires.
    private func set(_ ctx: JSContext, _ theme: String, choice: String? = nil) {
        if let choice { ctx.evaluateScript("store['pigtail-appearance'] = '\(choice)';") }
        ctx.evaluateScript("document.documentElement.dataset.theme = '\(theme)'; observers.forEach(function (o) { o.cb([]); });")
    }

    func testPostsTheChoiceAtDocumentEnd() {
        XCTAssertEqual(posts(page(theme: "dark", choice: "dark")), ["dark"])
        XCTAssertEqual(posts(page(theme: "dark", choice: "light")), ["light"])
    }

    /// The feedback loop: Système resolves through the web view's own
    /// prefers-color-scheme, which our forced appearance drives. The resolved
    /// data-theme must never be what the app follows.
    func testSystemChoiceIsPostedWhateverTheResolvedTheme() {
        XCTAssertEqual(posts(page(theme: "light", choice: "system")), ["system"])
        XCTAssertEqual(posts(page(theme: "dark", choice: "system")), ["system"])
    }

    func testAbsentUnknownOrBlockedStorageMeansSystem() {
        XCTAssertEqual(posts(page(theme: "light")), ["system"])
        XCTAssertEqual(posts(page(theme: "light", choice: "sepia")), ["system"])
        XCTAssertEqual(posts(page(theme: "light", storage: false)), ["system"])
    }

    func testFollowsAChangeOfChoice() {
        let ctx = page(theme: "dark", choice: "dark")
        set(ctx, "light", choice: "light")
        set(ctx, "light", choice: "system")
        XCTAssertEqual(posts(ctx), ["dark", "light", "system"])
    }

    func testTheSameChoiceTwicePostsOnce() {
        let ctx = page(theme: "light", choice: "light")
        set(ctx, "light")
        set(ctx, "light", choice: "system")
        set(ctx, "dark")
        XCTAssertEqual(posts(ctx), ["light", "system"])
    }

    func testObservesOnlyTheThemeAttributeOfTheRoot() {
        let ctx = page(theme: "dark")
        XCTAssertEqual(ctx.evaluateScript("observers.length").toInt32(), 1)
        XCTAssertTrue(ctx.evaluateScript("observers[0].target === document.documentElement").toBool())
        XCTAssertEqual(ctx.evaluateScript("JSON.stringify(observers[0].opts)").toString(),
                       #"{"attributes":true,"attributeFilter":["data-theme"]}"#)
    }

    /// The native daemon-down page (loadHTMLString) carries no data-theme.
    func testAPageWithoutTheThemeSendsNothing() {
        XCTAssertEqual(posts(page(theme: nil)), [])
    }

    func testNoHandlerNoError() {
        _ = page(theme: "dark", handler: false)
    }

    /// The contract the bridge reads, in the front's own sources: the root's
    /// data-theme attribute, and the saved choice (light, dark or system).
    func testTheFrontStillSetsTheAttribute() throws {
        let web = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("web/src/theme")
        let boot = try String(contentsOf: web.appendingPathComponent("boot.js"), encoding: .utf8)
        XCTAssertTrue(boot.contains("data-theme"))
        let appearance = try String(contentsOf: web.appendingPathComponent("appearance.ts"), encoding: .utf8)
        XCTAssertTrue(appearance.contains("pigtail-appearance"))
        XCTAssertEqual(Set(AppearanceChoice.allCases.map(\.rawValue)), ["light", "dark", "system"])
    }
}
