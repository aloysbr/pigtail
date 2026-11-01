import XCTest
@testable import WindowCore

final class DaemonDownPageTests: XCTestCase {
    func testStatusLiteralDaemonDown() {
        XCTAssertEqual(DaemonDownModel.status(variant: .daemonDown, secondsLeft: 4, fetching: false),
                       "Nouvel essai dans 4 s")
        XCTAssertEqual(DaemonDownModel.status(variant: .daemonDown, secondsLeft: 0, fetching: false),
                       "Nouvel essai en cours")
        XCTAssertEqual(DaemonDownModel.status(variant: .daemonDown, secondsLeft: 3, fetching: true),
                       "Nouvel essai en cours")
        // The page title already says it: the status line is the countdown only.
        for (secs, fetching) in [(4, false), (0, false), (3, true)] {
            XCTAssertFalse(DaemonDownModel.status(variant: .daemonDown, secondsLeft: secs, fetching: fetching).contains("ne répond pas"))
        }
    }

    func testStatusLiteralHttpOff() {
        for (s, f) in [(4, false), (0, false), (3, true)] {
            XCTAssertEqual(DaemonDownModel.status(variant: .httpOff, secondsLeft: s, fetching: f), "Recharger (⌘R) pour réessayer")
        }
    }

    func testStatusLiteralRefused() {
        for (s, f) in [(4, false), (0, true)] {
            XCTAssertEqual(DaemonDownModel.status(variant: .refused, secondsLeft: s, fetching: f), "Recharger (⌘R) pour réessayer")
        }
    }

    func testVariantFromTitle() {
        XCTAssertEqual(DaemonDownVariant(title: "pigtaild ne répond pas"), .daemonDown)
        XCTAssertEqual(DaemonDownVariant(title: "L'accès web de pigtaild est coupé"), .httpOff)
        XCTAssertEqual(DaemonDownVariant(title: "pigtaild refuse la connexion"), .refused)
    }

    func testDaemonDownPageHasNoDetail() {
        let html = DaemonDownPage.html(title: "pigtaild ne répond pas", status: "Nouvel essai dans 5 s", detail: nil)
        XCTAssertTrue(html.contains("<title>pigtaild ne répond pas</title>"))
        XCTAssertTrue(html.contains("<h1>pigtaild ne répond pas</h1>"))
        XCTAssertFalse(html.contains("class=\"detail\""))
        XCTAssertTrue(html.contains("prefers-color-scheme: dark"))
        XCTAssertFalse(html.contains("<script"))
        XCTAssertTrue(html.contains("id=\"status\""))
    }

    func testD8Pairs() {
        let pairs: [(String, String, String)] = [
            ("L'accès web de pigtaild est coupé", "pigtaild tourne sans écoute HTTP (--http \"\")", "pigtaild tourne sans écoute HTTP (--http &quot;&quot;)"),
            ("pigtaild refuse la connexion", "réponse 403", "réponse 403"),
            ("pigtaild refuse la connexion", "réponse illisible", "réponse illisible"),
        ]
        for (title, detail, escaped) in pairs {
            let html = DaemonDownPage.html(title: title, status: "Recharger (⌘R) pour réessayer", detail: detail)
            XCTAssertTrue(html.contains("<title>\(DaemonDownPage.escape(title))</title>"), title) // the apostrophe is escaped
            XCTAssertTrue(html.contains("<p class=\"detail\">\(escaped)</p>"), detail)
            XCTAssertTrue(html.contains(">Recharger (⌘R) pour réessayer</p>"))
        }
    }

    func testStatusIsEscaped() {
        let html = DaemonDownPage.html(title: "t", status: "<b>&\"x\"</b>", detail: nil)
        XCTAssertTrue(html.contains("&lt;b&gt;&amp;&quot;x&quot;&lt;/b&gt;"))
        XCTAssertFalse(html.contains("<b>"))
    }

    func testUpdateScriptCannotInject() {
        let js = DaemonDownPage.updateScript(status: "a\"; alert(1); \"</script><script>x")
        XCTAssertTrue(js.hasPrefix("document.getElementById('status').textContent = \""))
        XCTAssertTrue(js.hasSuffix("\";"))
        XCTAssertFalse(js.contains("</script>"))
        XCTAssertFalse(js.contains("a\";"))
        XCTAssertTrue(js.contains("a\\\"; alert(1); \\\""))
    }

    func testUpdateScriptPlainStatus() {
        XCTAssertEqual(DaemonDownPage.updateScript(status: "Nouvel essai dans 3 s"),
                       "document.getElementById('status').textContent = \"Nouvel essai dans 3 s\";")
    }
}
