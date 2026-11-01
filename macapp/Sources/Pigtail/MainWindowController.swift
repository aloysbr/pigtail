import AppCore
import AppKit
import WindowCore
import WebKit

/// The web view of the one window: it shows the web front, the login, or the
/// native daemon-down page. Glue only; the decisions live in Core.
@MainActor
final class MainWindowController: NSObject, WebViewControlling, WKNavigationDelegate, WKUIDelegate {
    let window: NSWindow
    let webView: WKWebView
    weak var driver: FlowDriver?

    /// The navigation load(login) returned: the login answers 303 to `/`,
    /// so its end is recognised by this object, never by the URL.
    private var loginNavigation: WKNavigation?
    /// The navigation of loadFront, to notice the daemon dying under it.
    private var frontNavigation: WKNavigation?

    /// The native page on screen, if any, and its countdown.
    private var page: (title: String, detail: String?)?
    private var secondsLeft = 0
    private var countdown: Timer?

    var cookieStore: WKHTTPCookieStore { webView.configuration.websiteDataStore.httpCookieStore }

    /// A web view for one opening of `window` (kept by the coordinator across closes).
    /// `seed`: the PreferenceSeedScript source, at document start in the main frame.
    init(window: NSWindow, dockIcon: DockIconBridge, appearance: AppearanceBridge, seed: String?) {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default() // persistent: the cookie survives a relaunch
        if let seed {
            config.userContentController.addUserScript(WKUserScript(
                source: seed, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        }
        dockIcon.install(on: config.userContentController)
        appearance.install(on: config.userContentController)
        webView = WKWebView(frame: .zero, configuration: config)
        self.window = window
        super.init()
        window.contentView = webView
        webView.navigationDelegate = self
        webView.uiDelegate = self
    }

    /// The window closed: stop the countdown and let the web view (and its
    /// WebContent process) go — the script handlers would otherwise retain it.
    func tearDown() {
        leavePage()
        loginNavigation = nil
        frontNavigation = nil
        webView.stopLoading()
        webView.navigationDelegate = nil
        webView.uiDelegate = nil
        let content = webView.configuration.userContentController
        content.removeAllUserScripts()
        content.removeAllScriptMessageHandlers()
        webView.removeFromSuperview()
        if window.contentView === webView { window.contentView = nil }
    }

    // MARK: WebViewControlling

    func loadFront(_ address: DaemonAddress) {
        leavePage()
        loginNavigation = nil
        frontNavigation = webView.load(URLRequest(url: address.url(path: "/")))
    }

    func loadLogin(_ address: DaemonAddress, code: String) {
        leavePage()
        frontNavigation = nil
        loginNavigation = webView.load(URLRequest(url: address.loginURL(code: code)))
    }

    func showDaemonDown(title: String, detail: String?) {
        loginNavigation = nil
        frontNavigation = nil
        secondsLeft = ConnectionFlow.retrySeconds
        let variant = DaemonDownVariant(title: title)
        let status = DaemonDownModel.status(variant: variant, secondsLeft: secondsLeft, fetching: false)
        if let page, page.title == title, page.detail == detail {
            // Same page: refresh the status line only (no reload flicker).
            webView.evaluateJavaScript(DaemonDownPage.updateScript(status: status))
        } else {
            page = (title, detail)
            webView.loadHTMLString(DaemonDownPage.html(title: title, status: status, detail: detail), baseURL: nil)
        }
        startCountdown(variant)
    }

    private func startCountdown(_ variant: DaemonDownVariant) {
        countdown?.invalidate()
        countdown = nil
        guard variant == .daemonDown else { return } // D18: no countdown on the D8 pages
        countdown = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.tickCountdown() }
        }
    }

    private func tickCountdown() {
        secondsLeft -= 1
        let status = DaemonDownModel.status(variant: .daemonDown, secondsLeft: secondsLeft, fetching: false)
        webView.evaluateJavaScript(DaemonDownPage.updateScript(status: status))
        if secondsLeft <= 0 {
            countdown?.invalidate()
            countdown = nil
        }
    }

    private func leavePage() {
        page = nil
        countdown?.invalidate()
        countdown = nil
    }

    // MARK: WKNavigationDelegate

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void) {
        guard let driver else { decisionHandler(.cancel); return }
        let opensNewWindow = action.targetFrame == nil
        let request = NavigationRequest(
            url: action.request.url,
            isMainFrame: action.targetFrame?.isMainFrame ?? true,
            opensNewWindow: opensNewWindow,
            kind: Self.kind(action.navigationType)
        )
        switch driver.policy.decide(request) {
        case .allow:
            if opensNewWindow {
                // target=_blank on the front's origin: load it here.
                webView.load(action.request)
                decisionHandler(.cancel)
            } else {
                decisionHandler(.allow)
            }
        case .gateSession:
            decisionHandler(.cancel)
            driver.handle(.sessionRefused)
        case .openExternally(let url):
            decisionHandler(.cancel)
            NSWorkspace.shared.open(url)
        case .cancel:
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
                 decisionHandler: @escaping @MainActor (WKNavigationResponsePolicy) -> Void) {
        guard let driver, let http = response.response as? HTTPURLResponse else {
            decisionHandler(.allow)
            return
        }
        switch ResponsePolicy.decide(url: http.url, status: http.statusCode, isMainFrame: response.isForMainFrame, origin: driver.address) {
        case .allow:
            decisionHandler(.allow)
        case .loginRefused:
            decisionHandler(.cancel)
            loginNavigation = nil
            driver.handle(.loginRefused)
        }
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        if let login = loginNavigation, navigation === login {
            loginNavigation = nil
            driver?.handle(.loginNavigationFinished)
            return
        }
        if navigation === frontNavigation { frontNavigation = nil }
        // Safety net: the daemon's « Lien expiré » page means a refused session.
        if page == nil, webView.title == "Lien expiré" {
            driver?.handle(.sessionRefused)
        }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        navigationFailed(navigation, error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        navigationFailed(navigation, error)
    }

    private func navigationFailed(_ navigation: WKNavigation!, _ error: Error) {
        if Self.isCancellation(error) { return }
        if let login = loginNavigation, navigation === login {
            loginNavigation = nil
            driver?.handle(.loginNavigationFailed)
        } else if let front = frontNavigation, navigation === front {
            // The daemon went away between the probe and the load: re-probe.
            frontNavigation = nil
            driver?.handle(.sessionRefused)
        }
    }

    private static func isCancellation(_ error: Error) -> Bool {
        let e = error as NSError
        if e.domain == NSURLErrorDomain && e.code == NSURLErrorCancelled { return true }
        // WebKitErrorFrameLoadInterruptedByPolicyChange: our own .cancel.
        return e.domain == "WebKitErrorDomain" && e.code == 102
    }

    private static func kind(_ t: WKNavigationType) -> NavigationKind {
        switch t {
        case .reload: return .reload
        case .linkActivated: return .linkActivated
        case .backForward: return .backForward
        default: return .other
        }
    }

    // MARK: WKUIDelegate

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        guard let driver else { return nil }
        let request = NavigationRequest(url: action.request.url, isMainFrame: true, opensNewWindow: true,
                                        kind: Self.kind(action.navigationType))
        switch driver.policy.decide(request) {
        case .allow, .gateSession:
            webView.load(action.request)
        case .openExternally(let url):
            NSWorkspace.shared.open(url)
        case .cancel:
            break
        }
        return nil // one window, always
    }
}
