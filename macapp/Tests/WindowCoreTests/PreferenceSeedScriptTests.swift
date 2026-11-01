import JavaScriptCore
import XCTest
@testable import WindowCore

final class PreferenceSeedScriptTests: XCTestCase {
    private func run(_ src: String, store: [String: String]) -> [String: String] {
        let ctx = JSContext()!
        let json = String(data: try! JSONSerialization.data(withJSONObject: store), encoding: .utf8)!
        ctx.evaluateScript("var store = \(json); var window = { localStorage: { getItem: function (k) { return k in store ? store[k] : null; }, setItem: function (k, v) { store[k] = String(v); } } };")
        ctx.evaluateScript(src)
        return ctx.objectForKeyedSubscript("store").toDictionary() as! [String: String]
    }

    func testSeedsAnEmptyStore() {
        let src = PreferenceSeedScript.source(palette: .ocean, appearance: .dark)!
        let out = run(src, store: [:])
        XCTAssertEqual(out["pigtail-palette"], "ocean")
        XCTAssertEqual(out["pigtail-appearance"], "dark")
    }
    func testNeverOverwritesTheWebChoice() {
        let src = PreferenceSeedScript.source(palette: .ocean, appearance: .dark)!
        let out = run(src, store: ["pigtail-palette": "terra", "pigtail-appearance": "light"])
        XCTAssertEqual(out["pigtail-palette"], "terra")
        XCTAssertEqual(out["pigtail-appearance"], "light")
    }
    func testNothingSavedNoScript() {
        XCTAssertNil(PreferenceSeedScript.source(palette: nil, appearance: nil))
    }
    func testWritesThroughWindowLocalStorage() {
        let src = PreferenceSeedScript.source(palette: .ocean, appearance: nil)!
        XCTAssertTrue(src.contains("window.localStorage"))
        XCTAssertNil(src.range(of: #"(?<![.\w])localStorage"#, options: .regularExpression), "bare localStorage")
    }
    func testThrowingStorageNeverThrows() {
        let ctx = JSContext()!
        var thrown = false
        ctx.exceptionHandler = { _, _ in thrown = true }
        ctx.evaluateScript("var window = { localStorage: { getItem: function () { throw new Error('x'); }, setItem: function () { throw new Error('x'); } } };")
        ctx.evaluateScript(PreferenceSeedScript.source(palette: .terra, appearance: .system)!)
        XCTAssertFalse(thrown)
    }
}
