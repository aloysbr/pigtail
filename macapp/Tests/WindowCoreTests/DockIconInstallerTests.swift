import WindowCore
import WebKit
import XCTest

/// The WebKit path, headless: an off-window WKWebView, the real user script,
/// the real handler. No window, no focus.
@MainActor
final class DockIconInstallerTests: XCTestCase {
    private func makeStore() -> DockIconStore {
        let suite = "DockIconInstallerTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        addTeardownBlock { defaults.removePersistentDomain(forName: suite) }
        return DockIconStore(defaults: defaults)
    }

    private func makeWebView(onPalette: @escaping @MainActor (ThemePalette) -> Void) -> (WKWebView, DockIconInstaller) {
        let installer = DockIconInstaller(theme: DockIconTheme(store: makeStore()), onPalette: onPalette)
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        installer.install(on: config.userContentController)
        return (WKWebView(frame: .init(x: 0, y: 0, width: 200, height: 200), configuration: config), installer)
    }

    func testTerraPageThenLiveChangeToOcean() async {
        // Océan is the store's default, so Terra is a change, then Océan another.
        let terra = expectation(description: "terra")
        let ocean = expectation(description: "ocean")
        var received: [ThemePalette] = []
        let (webView, installer) = makeWebView { palette in
            received.append(palette)
            if palette == .terra { terra.fulfill() }
            if palette == .ocean { ocean.fulfill() }
        }
        withExtendedLifetime(installer) {
            webView.loadHTMLString("<html data-theme-palette='terra'><body>x</body></html>", baseURL: nil)
        }
        await fulfillment(of: [terra], timeout: 10)
        _ = try? await webView.evaluateJavaScript("document.documentElement.setAttribute('data-theme-palette', 'ocean')")
        await fulfillment(of: [ocean], timeout: 10)
        XCTAssertEqual(received, [.terra, .ocean])
        withExtendedLifetime(installer) {}
    }

    /// Resolves once the web view has finished a navigation.
    private final class LoadWaiter: NSObject, WKNavigationDelegate {
        let loaded: XCTestExpectation
        init(_ loaded: XCTestExpectation) { self.loaded = loaded }
        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { loaded.fulfill() }
    }

    func testDaemonDownPageSendsNothing() async {
        var received: [ThemePalette] = []
        let terra = expectation(description: "terra control")
        let (webView, installer) = makeWebView { palette in
            received.append(palette)
            if palette == .terra { terra.fulfill() }
        }
        let downLoaded = expectation(description: "daemon-down page loaded")
        let waiter = LoadWaiter(downLoaded)
        webView.navigationDelegate = waiter
        webView.loadHTMLString(DaemonDownPage.html(title: "t", status: "s", detail: nil), baseURL: nil)
        await fulfillment(of: [downLoaded], timeout: 10)
        webView.navigationDelegate = nil // one-shot: the control page's load must not re-fulfil
        // The page is loaded and the user script has run: nothing was posted.
        XCTAssertEqual(received, [])

        // Positive control: the same web view, the same bridge, a terra page
        // delivers exactly one palette, so the silence above was not vacuous.
        webView.loadHTMLString("<html data-theme-palette='terra'><body>x</body></html>", baseURL: nil)
        await fulfillment(of: [terra], timeout: 10)
        XCTAssertEqual(received, [.terra])
        withExtendedLifetime((installer, waiter)) {}
    }
}
