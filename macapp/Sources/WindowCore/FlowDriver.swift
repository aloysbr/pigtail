import Foundation

/// Reads the session cookie the web view holds for an address.
@MainActor
public protocol CookieReading: AnyObject {
    func sessionCookie(for address: DaemonAddress) async -> String?
}

/// What the flow asks of the window.
@MainActor
public protocol WebViewControlling: AnyObject {
    func loadFront(_ address: DaemonAddress)
    func loadLogin(_ address: DaemonAddress, code: String)
    func showDaemonDown(title: String, detail: String?)
}

/// One pending silent retry at a time.
@MainActor
public protocol RetryScheduling: AnyObject {
    func schedule(seconds: Int, _ fire: @escaping @MainActor () -> Void)
    func cancel()
}

/// Binds the pure ConnectionFlow to its ports: runs each effect, feeds each
/// result back as an event. All on the main actor.
@MainActor
public final class FlowDriver {
    private var flow: ConnectionFlow
    private let minting: any LoginCodeMinting
    private let probing: any SessionProbing
    private let cookies: any CookieReading
    private weak var view: (any WebViewControlling)?
    private let timer: any RetryScheduling
    private let store: AddressStore
    private var tasks: [Task<Void, Never>] = []

    /// The navigation policy of the current origin, rebuilt at each saved address.
    public private(set) var policy: NavigationPolicy

    public init(minting: any LoginCodeMinting, probing: any SessionProbing, cookies: any CookieReading,
                view: any WebViewControlling, timer: any RetryScheduling, store: AddressStore) {
        let address = store.load()
        self.flow = ConnectionFlow(address: address)
        self.policy = NavigationPolicy(origin: address)
        self.minting = minting
        self.probing = probing
        self.cookies = cookies
        self.view = view
        self.timer = timer
        self.store = store
    }

    public var state: FlowState { flow.state }
    public var address: DaemonAddress { flow.address }
    public var mintsThisCycle: Int { flow.mintsThisCycle }

    public func handle(_ event: FlowEvent) {
        for effect in flow.handle(event) {
            run(effect)
        }
    }

    /// Waits until no effect is in flight (tests; the app never needs it).
    public func settle() async {
        while !tasks.isEmpty {
            let pending = tasks
            tasks = []
            for t in pending { await t.value }
        }
    }

    private func run(_ effect: FlowEffect) {
        switch effect {
        case .probe:
            let address = flow.address
            spawn { [cookies, probing] in
                let cookie = await cookies.sessionCookie(for: address)
                let outcome = await probing.check(address: address, cookieValue: cookie)
                return .probed(outcome)
            }
        case .mint:
            spawn { [minting] in
                .minted(await Self.mintResult(minting))
            }
        case .loadFront(let a):
            view?.loadFront(a)
        case .loadLogin(let a, let code):
            view?.loadLogin(a, code: code)
        case .showDaemonDown(let title, let detail):
            view?.showDaemonDown(title: title, detail: detail)
        case .scheduleRetry(let seconds):
            timer.schedule(seconds: seconds) { [weak self] in self?.handle(.retryFired) }
        case .cancelRetry:
            timer.cancel()
        case .saveAddress(let a):
            store.save(a)
            policy = NavigationPolicy(origin: a)
        }
    }

    private nonisolated static func mintResult(_ minting: any LoginCodeMinting) async -> Result<LoginCode, LoginCodeError> {
        do throws(LoginCodeError) {
            return .success(try await minting.mint())
        } catch {
            return .failure(error)
        }
    }

    private func spawn(_ work: @escaping @MainActor () async -> FlowEvent) {
        let t = Task { @MainActor [weak self] in
            let event = await work()
            self?.handle(event)
        }
        tasks.append(t)
    }
}
