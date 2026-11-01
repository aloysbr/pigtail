import XCTest
@testable import WindowCore

// MARK: fakes

final class FakeMinter: LoginCodeMinting, @unchecked Sendable {
    private let lock = NSLock()
    private var _script: @Sendable () -> Result<LoginCode, LoginCodeError>
    private var _calls = 0
    private var _codes = 0

    init(_ script: @escaping @Sendable () -> Result<LoginCode, LoginCodeError>) { _script = script }

    var script: @Sendable () -> Result<LoginCode, LoginCodeError> {
        get { lock.withLock { _script } }
        set { lock.withLock { _script = newValue } }
    }
    /// Requests made on the socket.
    var calls: Int { lock.withLock { _calls } }
    /// Codes actually handed out.
    var codes: Int { lock.withLock { _codes } }

    func mint() async throws(LoginCodeError) -> LoginCode {
        let r: Result<LoginCode, LoginCodeError> = lock.withLock {
            _calls += 1
            let r = _script()
            if case .success = r { _codes += 1 }
            return r
        }
        return try r.get()
    }
}

final class FakeProber: SessionProbing, @unchecked Sendable {
    private let lock = NSLock()
    private var _script: @Sendable (DaemonAddress, String?) -> ProbeOutcome

    init(_ script: @escaping @Sendable (DaemonAddress, String?) -> ProbeOutcome) { _script = script }

    var script: @Sendable (DaemonAddress, String?) -> ProbeOutcome {
        get { lock.withLock { _script } }
        set { lock.withLock { _script = newValue } }
    }

    func check(address: DaemonAddress, cookieValue: String?) async -> ProbeOutcome {
        lock.withLock { _script }(address, cookieValue)
    }

    /// A daemon answering on `at`: valid with a cookie, invalid without.
    static func up(at: DaemonAddress? = nil) -> @Sendable (DaemonAddress, String?) -> ProbeOutcome {
        { a, c in
            if let at, a != at { return .unreachable }
            return c == nil ? .invalid : .valid
        }
    }
}

@MainActor
final class FakeCookies: CookieReading {
    var value: String?
    init(_ v: String?) { value = v }
    func sessionCookie(for address: DaemonAddress) async -> String? { value }
}

@MainActor
final class FakeView: WebViewControlling {
    enum Call: Equatable {
        case loadFront(DaemonAddress)
        case loadLogin(DaemonAddress, String)
        case daemonDown(String, String?)
    }
    var calls: [Call] = []
    func loadFront(_ address: DaemonAddress) { calls.append(.loadFront(address)) }
    func loadLogin(_ address: DaemonAddress, code: String) { calls.append(.loadLogin(address, code)) }
    func showDaemonDown(title: String, detail: String?) { calls.append(.daemonDown(title, detail)) }
    var daemonDownPages: [Call] { calls.filter { if case .daemonDown = $0 { return true } else { return false } } }
}

@MainActor
final class FakeTimer: RetryScheduling {
    private var pending: (@MainActor () -> Void)?
    var scheduled: [Int] = []
    var armed: Bool { pending != nil }
    func schedule(seconds: Int, _ fire: @escaping @MainActor () -> Void) { scheduled.append(seconds); pending = fire }
    func cancel() { pending = nil }
    func fire() { let p = pending; pending = nil; p?() }
}

// MARK: tests

@MainActor
final class FlowDriverTests: XCTestCase {
    private let a7800 = DaemonAddress(httpAddr: "127.0.0.1:7800")!
    private let a7811 = DaemonAddress(httpAddr: "127.0.0.1:7811")!

    private var view: FakeView!
    private var timer: FakeTimer!
    private var cookies: FakeCookies!
    private var store: AddressStore!

    override func setUp() async throws {
        view = FakeView()
        timer = FakeTimer()
        cookies = FakeCookies(nil)
        let name = "FlowDriverTests.\(UUID().uuidString)"
        store = AddressStore(defaults: UserDefaults(suiteName: name)!)
        addTeardownBlock { UserDefaults(suiteName: name)?.removePersistentDomain(forName: name) }
    }

    private func driver(_ minter: FakeMinter, _ prober: FakeProber) -> FlowDriver {
        FlowDriver(minting: minter, probing: prober, cookies: cookies, view: view, timer: timer, store: store)
    }

    private static func ok(_ addr: String = "127.0.0.1:7800", _ code: String = "c1") -> @Sendable () -> Result<LoginCode, LoginCodeError> {
        { .success(LoginCode(code: code, httpAddr: addr)) }
    }

    private func tick(_ d: FlowDriver) async {
        timer.fire()
        await d.settle()
    }

    /// The web view finishing the login: the cookie lands, then the event.
    private func finishLogin(_ d: FlowDriver) async {
        cookies.value = "session"
        d.handle(.loginNavigationFinished)
        await d.settle()
    }

    func testFirstLoginLoadsLoginThenShowing() async {
        let minter = FakeMinter(Self.ok())
        let d = driver(minter, FakeProber(FakeProber.up()))
        d.handle(.start)
        await d.settle()
        XCTAssertEqual(view.calls, [.loadLogin(a7800, "c1")])
        XCTAssertEqual(d.state, .loggingIn)
        await finishLogin(d)
        XCTAssertEqual(d.state, .showing)
        XCTAssertEqual(view.calls, [.loadLogin(a7800, "c1")]) // no front load first, no expired page
    }

    func testValidCookieMintsNothing() async {
        cookies.value = "session"
        let minter = FakeMinter(Self.ok())
        let d = driver(minter, FakeProber(FakeProber.up()))
        d.handle(.start)
        await d.settle()
        XCTAssertEqual(view.calls, [.loadFront(a7800)])
        XCTAssertEqual(minter.calls, 0)
        XCTAssertEqual(d.state, .showing)
    }

    func testSocketUnreachableShowsDaemonDownThenRetryLoadsFront() async {
        cookies.value = "session"
        let minter = FakeMinter { .failure(.daemonUnreachable) }
        let prober = FakeProber { _, _ in .unreachable }
        let d = driver(minter, prober)
        d.handle(.start)
        await d.settle()
        XCTAssertEqual(view.calls, [.daemonDown("pigtaild ne répond pas", nil)])
        XCTAssertTrue(timer.armed)
        XCTAssertEqual(timer.scheduled.last, 5)
        prober.script = FakeProber.up()
        await tick(d)
        XCTAssertEqual(view.calls.last, .loadFront(a7800))
        XCTAssertEqual(d.state, .showing)
    }

    func testSessionRefusedWhileShowingReprobesWithoutDaemonDownPage() async {
        cookies.value = "session"
        let minter = FakeMinter(Self.ok())
        let d = driver(minter, FakeProber(FakeProber.up()))
        d.handle(.start)
        await d.settle()
        XCTAssertEqual(d.state, .showing)
        cookies.value = nil // the daemon's session secret changed
        d.handle(.sessionRefused)
        await d.settle()
        XCTAssertEqual(view.calls.last, .loadLogin(a7800, "c1"))
        await finishLogin(d)
        XCTAssertEqual(d.state, .showing)
        XCTAssertEqual(view.daemonDownPages, [])
    }

    func testUnreachableProbeSocketCheckOutcomes() async {
        do { // socket error
            let d = driver(FakeMinter { .failure(.daemonUnreachable) }, FakeProber { _, _ in .unreachable })
            d.handle(.start)
            await d.settle()
            XCTAssertEqual(view.calls.last, .daemonDown("pigtaild ne répond pas", nil))
        }
        do { // 409
            view.calls = []
            let d = driver(FakeMinter { .failure(.httpOff) }, FakeProber { _, _ in .unreachable })
            d.handle(.start)
            await d.settle()
            XCTAssertEqual(view.calls.last, .daemonDown("L'accès web de pigtaild est coupé", "pigtaild tourne sans écoute HTTP (--http \"\")"))
        }
        do { // 200 with a new http_addr
            view.calls = []
            let minter = FakeMinter(Self.ok("127.0.0.1:7811", "fresh"))
            let d = driver(minter, FakeProber(FakeProber.up(at: a7811)))
            d.handle(.start)
            await d.settle()
            XCTAssertEqual(view.calls, [.loadLogin(a7811, "fresh")])
            XCTAssertEqual(store.load(), a7811)
            XCTAssertEqual(d.policy.origin, a7811)
            await finishLogin(d)
            XCTAssertEqual(d.state, .showing)
            XCTAssertEqual(minter.calls, 1)
        }
    }

    func testRefusedAfterFreshCodeShowsRefusedAndRetryMintsNothing() async {
        let minter = FakeMinter(Self.ok())
        let d = driver(minter, FakeProber { _, _ in .invalid })
        d.handle(.start)
        await d.settle()
        await finishLogin(d)
        XCTAssertEqual(view.calls.last, .daemonDown("pigtaild refuse la connexion", "réponse 401"))
        XCTAssertEqual(minter.calls, 1)
        for _ in 0..<3 { await tick(d) }
        XCTAssertEqual(minter.calls, 1)
        XCTAssertEqual(d.state, .failed)
    }

    func testStaleAddressAlsoUnreachableMintsAtMostTwice() async {
        let minter = FakeMinter(Self.ok("127.0.0.1:7811"))
        let d = driver(minter, FakeProber { _, _ in .unreachable })
        d.handle(.start)
        await d.settle()
        for _ in 0..<12 {
            if d.state == .loggingIn {
                d.handle(.loginNavigationFailed) // the new address does not answer either
                await d.settle()
            }
            await tick(d)
        }
        XCTAssertLessThanOrEqual(minter.calls, 2)
    }

    func testNoCookieDaemonRestartedReachesShowingWithoutReload() async {
        let minter = FakeMinter { .failure(.daemonUnreachable) }
        let prober = FakeProber { _, _ in .unreachable }
        let d = driver(minter, prober)
        d.handle(.start)
        await d.settle()
        await tick(d)
        await tick(d)
        XCTAssertEqual(d.state, .daemonDown)
        prober.script = FakeProber.up()
        minter.script = Self.ok()
        await tick(d)
        XCTAssertEqual(d.state, .loggingIn)
        await finishLogin(d)
        XCTAssertEqual(d.state, .showing)
    }

    func testValidCookieDaemonRestartedShowsWithZeroMints() async {
        cookies.value = "session"
        let minter = FakeMinter { .failure(.daemonUnreachable) }
        let prober = FakeProber { _, _ in .unreachable }
        let d = driver(minter, prober)
        d.handle(.start)
        await d.settle()
        await tick(d)
        XCTAssertEqual(d.state, .daemonDown)
        XCTAssertEqual(d.mintsThisCycle, 0)
        prober.script = FakeProber.up()
        await tick(d)
        XCTAssertEqual(d.state, .showing)
        XCTAssertEqual(minter.codes, 0)
        XCTAssertEqual(d.mintsThisCycle, 0)
        XCTAssertEqual(view.calls.last, .loadFront(a7800))
    }

    func testSocketCheckCodeUsedForLoginExactlyOneMint() async {
        let minter = FakeMinter(Self.ok("127.0.0.1:7811", "once"))
        let d = driver(minter, FakeProber(FakeProber.up(at: a7811)))
        d.handle(.start)
        await d.settle()
        XCTAssertEqual(view.calls, [.loadLogin(a7811, "once")])
        await finishLogin(d)
        XCTAssertEqual(d.state, .showing)
        XCTAssertEqual(minter.calls, 1)
    }
}
