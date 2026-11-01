import WindowCore
import WebKit
import XCTest

/// The WebKit path, headless: an off-window WKWebView, the real user script,
/// the real handler. No window, no focus.
@MainActor
final class AppearanceInstallerTests: XCTestCase {
    private func makeStore() -> WindowAppearanceStore {
        let suite = "AppearanceInstallerTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        addTeardownBlock { defaults.removePersistentDomain(forName: suite) }
        return WindowAppearanceStore(defaults: defaults)
    }

    private func makeWebView(onAppearance: @escaping @MainActor (AppearanceChoice) -> Void) -> (WKWebView, AppearanceInstaller) {
        let installer = AppearanceInstaller(appearance: WindowAppearance(store: makeStore()), onAppearance: onAppearance)
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        installer.install(on: config.userContentController)
        return (WKWebView(frame: .init(x: 0, y: 0, width: 200, height: 200), configuration: config), installer)
    }

    private static let origin = URL(string: "https://front.test/")

    /// A page as the web front leaves it: the saved choice, then the resolved theme.
    private static func page(theme: String, choice: String) -> String {
        "<html data-theme='\(theme)'><body><script>localStorage.setItem('pigtail-appearance', '\(choice)')</script>x</body></html>"
    }

    func testDarkPageThenLiveChangeToLight() async {
        let dark = expectation(description: "dark")
        let light = expectation(description: "light")
        var received: [AppearanceChoice] = []
        let (webView, installer) = makeWebView { appearance in
            received.append(appearance)
            if appearance == .dark { dark.fulfill() }
            if appearance == .light { light.fulfill() }
        }
        withExtendedLifetime(installer) {
            webView.loadHTMLString(Self.page(theme: "dark", choice: "dark"), baseURL: Self.origin)
        }
        await fulfillment(of: [dark], timeout: 10)
        _ = try? await webView.evaluateJavaScript("localStorage.setItem('pigtail-appearance', 'light'); document.documentElement.setAttribute('data-theme', 'light')")
        await fulfillment(of: [light], timeout: 10)
        XCTAssertEqual(received, [.dark, .light])
        withExtendedLifetime(installer) {}
    }

    /// The bug: a light-resolved page whose choice is Système posts system.
    func testSystemChoiceOnALightResolvedPage() async {
        let system = expectation(description: "system")
        var received: [AppearanceChoice] = []
        let (webView, installer) = makeWebView { appearance in
            received.append(appearance)
            if appearance == .system { system.fulfill() }
        }
        withExtendedLifetime(installer) {
            webView.loadHTMLString(Self.page(theme: "light", choice: "system"), baseURL: Self.origin)
        }
        await fulfillment(of: [system], timeout: 10)
        XCTAssertEqual(received, [.system])
        withExtendedLifetime(installer) {}
    }

    /// Resolves once the web view has finished a navigation.
    private final class LoadWaiter: NSObject, WKNavigationDelegate {
        let loaded: XCTestExpectation
        init(_ loaded: XCTestExpectation) { self.loaded = loaded }
        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { loaded.fulfill() }
    }

    func testDaemonDownPageSendsNothing() async {
        var received: [AppearanceChoice] = []
        let dark = expectation(description: "dark control")
        let (webView, installer) = makeWebView { appearance in
            received.append(appearance)
            if appearance == .dark { dark.fulfill() }
        }
        let downLoaded = expectation(description: "daemon-down page loaded")
        let waiter = LoadWaiter(downLoaded)
        webView.navigationDelegate = waiter
        webView.loadHTMLString(DaemonDownPage.html(title: "t", status: "s", detail: nil), baseURL: nil)
        await fulfillment(of: [downLoaded], timeout: 10)
        webView.navigationDelegate = nil // one-shot: the control page's load must not re-fulfil
        XCTAssertEqual(received, [])

        // Positive control: the same web view, the same bridge, a dark page
        // delivers exactly one appearance, so the silence above was not vacuous.
        webView.loadHTMLString(Self.page(theme: "dark", choice: "dark"), baseURL: Self.origin)
        await fulfillment(of: [dark], timeout: 10)
        XCTAssertEqual(received, [.dark])
        withExtendedLifetime((installer, waiter)) {}
    }
}
