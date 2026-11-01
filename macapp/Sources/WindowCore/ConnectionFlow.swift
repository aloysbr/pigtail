import Foundation

/// Why the session is being probed.
public enum ProbeReason: Equatable, Sendable {
    /// First probe of a cycle (launch, or View › Recharger from a page).
    case launch
    /// The web front said its session was refused while showing.
    case gate
    /// A silent retry from the daemon-down page (nothing refused yet).
    case retry
    /// A silent retry from a refusal page: probe only, never a code.
    case recheck
    /// Right after the login navigation finished.
    case afterLogin
}

public enum FlowState: Equatable, Sendable {
    case idle
    case probing(ProbeReason)
    /// A code is being asked on the socket after a refused session.
    case minting
    /// A code is being asked on the socket because the probe found no HTTP
    /// answer: it tells a stopped daemon (socket error) from one whose HTTP
    /// is off (409) or whose address moved (200 + a new http_addr).
    case socketCheck
    /// The login URL is loading in the web view.
    case loggingIn
    case showing
    /// Page D2: pigtaild does not answer; silent retry every 5 s.
    case daemonDown
    /// A D8 page (HTTP off, or refused); silent probe-only retry every 5 s.
    case failed
}

public enum FlowEvent: Equatable, Sendable {
    case start
    case reloadRequested
    case sessionRefused
    case probed(ProbeOutcome)
    case minted(Result<LoginCode, LoginCodeError>)
    case loginNavigationFinished
    case loginNavigationFailed
    case loginRefused
    case retryFired
    /// The window closed: the flow goes idle until the next `start`.
    case windowClosed
}

public enum FlowEffect: Equatable, Sendable {
    case probe
    case mint
    case loadFront(DaemonAddress)
    case loadLogin(DaemonAddress, code: String)
    case showDaemonDown(title: String, detail: String?)
    case scheduleRetry(seconds: Int)
    case cancelRetry
    case saveAddress(DaemonAddress)
}

/// The connection state machine: pure, no I/O. The driver runs its effects
/// and feeds back their results as events.
///
/// Anti-loop rules (plan task 4):
/// 1. Every mint (socket check included) needs mintsThisCycle < 2; a mint
///    the cap blocks goes to `failed` with no request. A mint that found no
///    daemon (.daemonUnreachable, .timeout) gives its unit back.
/// 2. The counter goes back to 0 only in `showing` and on reloadRequested.
/// 3. A retry from `daemonDown` takes the normal path; a retry from `failed`
///    only probes and never mints.
/// 4. The socket check's code is used for the login at once.
/// 5. After the login, an unreachable probe or a failed login navigation
///    leads to `daemonDown` with no new mint.
/// 6. A valid gate probe reloads the web front at most `maxGateReloadsPerCycle`
///    times per cycle; past it, the daemon-down page (retry every 5 s). The
///    counter goes back to 0 on reloadRequested and on a valid probe that is
///    not a gate probe (never on the gate probe itself, or it would not bound).
public struct ConnectionFlow: Sendable {
    public static let unreachableTitle = "pigtaild ne répond pas"
    public static let httpOffTitle = "L'accès web de pigtaild est coupé"
    public static let httpOffDetail = "pigtaild tourne sans écoute HTTP (--http \"\")"
    public static let refusedTitle = "pigtaild refuse la connexion"
    public static let unreadableDetail = "réponse illisible"
    public static let maxMintsPerCycle = 2
    public static let retrySeconds = 5
    public static let maxGateReloadsPerCycle = 2

    public static func refusedDetail(_ status: Int) -> String { "réponse \(status)" }

    public private(set) var state: FlowState
    public private(set) var address: DaemonAddress
    public private(set) var mintsThisCycle: Int
    public private(set) var gateReloadsThisCycle: Int

    public init(address: DaemonAddress) {
        self.init(address: address, state: .idle, mintsThisCycle: 0, gateReloadsThisCycle: 0)
    }

    /// Test seam: start from any state.
    init(address: DaemonAddress, state: FlowState, mintsThisCycle: Int, gateReloadsThisCycle: Int = 0) {
        self.address = address
        self.state = state
        self.mintsThisCycle = mintsThisCycle
        self.gateReloadsThisCycle = gateReloadsThisCycle
    }

    public mutating func handle(_ e: FlowEvent) -> [FlowEffect] {
        switch (state, e) {
        case (.idle, .start):
            state = .probing(.launch)
            return [.probe]

        case (.probing(let reason), .probed(let outcome)):
            return probed(reason, outcome)

        case (.minting, .minted(let result)), (.socketCheck, .minted(let result)):
            return minted(result)

        case (.loggingIn, .loginNavigationFinished):
            state = .probing(.afterLogin)
            return [.probe]
        case (.loggingIn, .loginNavigationFailed):
            return goDaemonDown()
        case (.loggingIn, .loginRefused):
            return mintOrFail(next: .minting, refusedDetail: Self.refusedDetail(410))

        case (.daemonDown, .retryFired):
            state = .probing(.retry)
            return [.probe]
        case (.failed, .retryFired):
            state = .probing(.recheck)
            return [.probe]

        case (.showing, .sessionRefused):
            state = .probing(.gate)
            return [.probe]
        case (.showing, .reloadRequested):
            mintsThisCycle = 0
            gateReloadsThisCycle = 0
            state = .probing(.gate)
            return [.probe]
        case (.daemonDown, .reloadRequested), (.failed, .reloadRequested):
            mintsThisCycle = 0
            gateReloadsThisCycle = 0
            state = .probing(.launch)
            return [.probe]

        case (_, .windowClosed):
            // Closing the window frees the web view: any pending retry goes, and
            // a late result (idle drops it) no longer reaches a view.
            state = .idle
            mintsThisCycle = 0
            gateReloadsThisCycle = 0
            return [.cancelRetry]

        default:
            // Busy states ignore sessionRefused/reloadRequested (no burst);
            // a stale result for another state is dropped.
            return []
        }
    }

    private mutating func probed(_ reason: ProbeReason, _ outcome: ProbeOutcome) -> [FlowEffect] {
        switch (reason, outcome) {
        case (.afterLogin, .valid):
            gateReloadsThisCycle = 0
            goShowing()
            return []
        case (.gate, .valid):
            guard gateReloadsThisCycle < Self.maxGateReloadsPerCycle else {
                return goDaemonDown()
            }
            gateReloadsThisCycle += 1
            goShowing()
            return [.cancelRetry, .loadFront(address)]
        case (_, .valid):
            gateReloadsThisCycle = 0
            goShowing()
            return [.cancelRetry, .loadFront(address)]

        case (.afterLogin, .invalid), (.recheck, .invalid):
            return goFailed(Self.refusedTitle, Self.refusedDetail(401))
        case (_, .invalid):
            return mintOrFail(next: .minting, refusedDetail: Self.refusedDetail(401))

        case (_, .hostRefused):
            return goFailed(Self.refusedTitle, Self.refusedDetail(403))
        case (_, .unexpected(let c)):
            return goFailed(Self.refusedTitle, Self.refusedDetail(c))

        case (.afterLogin, .unreachable):
            return goDaemonDown()
        case (.recheck, .unreachable):
            // The refusal page stays as it is: never another cause shown.
            state = .failed
            return [.scheduleRetry(seconds: Self.retrySeconds)]
        case (_, .unreachable):
            return mintOrFail(next: .socketCheck, refusedDetail: nil)
        }
    }

    private mutating func minted(_ result: Result<LoginCode, LoginCodeError>) -> [FlowEffect] {
        switch result {
        case .success(let code):
            guard let addr = DaemonAddress(httpAddr: code.httpAddr) else {
                return goFailed(Self.refusedTitle, Self.unreadableDetail)
            }
            address = addr
            state = .loggingIn
            return [.saveAddress(addr), .loadLogin(addr, code: code.code)]
        case .failure(.daemonUnreachable), .failure(.timeout):
            // Nothing was struck on the daemon: the unit comes back.
            mintsThisCycle = max(0, mintsThisCycle - 1)
            return goDaemonDown()
        case .failure(.httpOff):
            return goFailed(Self.httpOffTitle, Self.httpOffDetail)
        case .failure(.forbidden):
            return goFailed(Self.refusedTitle, Self.refusedDetail(403))
        case .failure(.server(let c)):
            return goFailed(Self.refusedTitle, Self.refusedDetail(c))
        case .failure(.malformed):
            return goFailed(Self.refusedTitle, Self.unreadableDetail)
        }
    }

    /// Rule 1: mint if the cycle allows it, else the refusal page.
    private mutating func mintOrFail(next: FlowState, refusedDetail: String?) -> [FlowEffect] {
        guard mintsThisCycle < Self.maxMintsPerCycle else {
            return goFailed(Self.refusedTitle, refusedDetail)
        }
        mintsThisCycle += 1
        state = next
        return [.mint]
    }

    private mutating func goShowing() {
        state = .showing
        mintsThisCycle = 0
    }

    private mutating func goFailed(_ title: String, _ detail: String?) -> [FlowEffect] {
        state = .failed
        return [.showDaemonDown(title: title, detail: detail), .scheduleRetry(seconds: Self.retrySeconds)]
    }

    private mutating func goDaemonDown() -> [FlowEffect] {
        state = .daemonDown
        return [.showDaemonDown(title: Self.unreachableTitle, detail: nil), .scheduleRetry(seconds: Self.retrySeconds)]
    }
}
