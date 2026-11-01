import AppKit
import WindowCore
import WebKit

/// Wires the FlowDriver to the real world: the socket client, the HTTP
/// probe, the web view's cookie store, the window and a Timer.
@MainActor
final class FlowRunner {
    private let driver: FlowDriver

    init(controller: MainWindowController) {
        let socket = SocketPath.current()
        driver = FlowDriver(
            minting: LoginCodeClient(socketPath: socket, timeout: 3),
            probing: SessionProbe(),
            cookies: WebViewCookies(store: controller.cookieStore),
            view: controller,
            timer: RetryTimer(),
            store: AddressStore()
        )
        controller.driver = driver
    }

    func start() { driver.handle(.start) }
    func reload() { driver.handle(.reloadRequested) }
    func handle(_ event: FlowEvent) { driver.handle(event) }
}

/// The session cookie as WKWebView holds it (persistent data store).
@MainActor
final class WebViewCookies: CookieReading {
    private let store: WKHTTPCookieStore

    init(store: WKHTTPCookieStore) { self.store = store }

    func sessionCookie(for address: DaemonAddress) async -> String? {
        let all = await store.allCookies()
        return all.first { c in
            c.name == SessionProbe.cookieName && c.domain.lowercased() == address.host
        }?.value
    }
}

/// One pending retry at a time, on the main run loop.
@MainActor
final class RetryTimer: RetryScheduling {
    private var timer: Timer?

    func schedule(seconds: Int, _ fire: @escaping @MainActor () -> Void) {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: TimeInterval(seconds), repeats: false) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.timer = nil
                fire()
            }
        }
    }

    func cancel() {
        timer?.invalidate()
        timer = nil
    }
}
