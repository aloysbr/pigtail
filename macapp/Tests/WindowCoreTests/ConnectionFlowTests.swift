import XCTest
@testable import WindowCore

final class ConnectionFlowTests: XCTestCase {
    private let addr = DaemonAddress(httpAddr: "127.0.0.1:7800")!
    private let newAddr = DaemonAddress(httpAddr: "127.0.0.1:7811")!
    private let refused = ConnectionFlow.refusedTitle
    private let retry = FlowEffect.scheduleRetry(seconds: 5)

    private func flow(_ state: FlowState, mints: Int = 0, reloads: Int = 0) -> ConnectionFlow {
        ConnectionFlow(address: addr, state: state, mintsThisCycle: mints, gateReloadsThisCycle: reloads)
    }

    private func ok(_ a: String = "127.0.0.1:7800", code: String = "c1") -> FlowEvent {
        .minted(.success(LoginCode(code: code, httpAddr: a)))
    }

    private func fail(_ t: String, _ d: String?) -> [FlowEffect] {
        [.showDaemonDown(title: t, detail: d), retry]
    }

    private let daemonDownPage: [FlowEffect] = [.showDaemonDown(title: "pigtaild ne répond pas", detail: nil), .scheduleRetry(seconds: 5)]

    // MARK: labels

    func testLabels() {
        XCTAssertEqual(ConnectionFlow.unreachableTitle, "pigtaild ne répond pas")
        XCTAssertEqual(ConnectionFlow.httpOffTitle, "L'accès web de pigtaild est coupé")
        XCTAssertEqual(ConnectionFlow.httpOffDetail, "pigtaild tourne sans écoute HTTP (--http \"\")")
        XCTAssertEqual(ConnectionFlow.refusedTitle, "pigtaild refuse la connexion")
        XCTAssertEqual(ConnectionFlow.refusedDetail(403), "réponse 403")
    }

    // MARK: rows

    func testIdleStartProbes() {
        var f = ConnectionFlow(address: addr)
        XCTAssertEqual(f.handle(.start), [.probe])
        XCTAssertEqual(f.state, .probing(.launch))
    }

    func testValidProbeShowsWithoutMint() {
        for r in [ProbeReason.launch, .gate, .retry, .recheck] {
            var f = flow(.probing(r), mints: 1)
            XCTAssertEqual(f.handle(.probed(.valid)), [.cancelRetry, .loadFront(addr)], "\(r)")
            XCTAssertEqual(f.state, .showing)
            XCTAssertEqual(f.mintsThisCycle, 0)
        }
    }

    func testInvalidProbeMints() {
        for r in [ProbeReason.launch, .gate, .retry] {
            var f = flow(.probing(r))
            XCTAssertEqual(f.handle(.probed(.invalid)), [.mint], "\(r)")
            XCTAssertEqual(f.state, .minting)
            XCTAssertEqual(f.mintsThisCycle, 1)
        }
    }

    func testInvalidProbeAtCapFailsWithoutRequest() {
        var f = flow(.probing(.gate), mints: 2)
        XCTAssertEqual(f.handle(.probed(.invalid)), fail(refused, "réponse 401"))
        XCTAssertEqual(f.state, .failed)
        XCTAssertEqual(f.mintsThisCycle, 2)
    }

    func testRecheckInvalidStaysFailedWithoutMint() {
        var f = flow(.probing(.recheck))
        XCTAssertEqual(f.handle(.probed(.invalid)), fail(refused, "réponse 401"))
        XCTAssertEqual(f.state, .failed)
        XCTAssertEqual(f.mintsThisCycle, 0)
    }

    func testAfterLoginValidShows() {
        var f = flow(.probing(.afterLogin), mints: 1)
        XCTAssertEqual(f.handle(.probed(.valid)), [])
        XCTAssertEqual(f.state, .showing)
        XCTAssertEqual(f.mintsThisCycle, 0)
    }

    func testAfterLoginInvalidFails() {
        var f = flow(.probing(.afterLogin), mints: 1)
        XCTAssertEqual(f.handle(.probed(.invalid)), fail(refused, "réponse 401"))
        XCTAssertEqual(f.state, .failed)
    }

    func testAfterLoginUnreachableIsDaemonDownWithoutMint() {
        var f = flow(.probing(.afterLogin), mints: 1)
        XCTAssertEqual(f.handle(.probed(.unreachable)), daemonDownPage)
        XCTAssertEqual(f.state, .daemonDown)
        XCTAssertEqual(f.mintsThisCycle, 1)
    }

    func testUnreachableProbeChecksSocket() {
        for r in [ProbeReason.launch, .gate, .retry] {
            var f = flow(.probing(r))
            XCTAssertEqual(f.handle(.probed(.unreachable)), [.mint], "\(r)")
            XCTAssertEqual(f.state, .socketCheck)
            XCTAssertEqual(f.mintsThisCycle, 1)
        }
    }

    func testUnreachableProbeAtCapFails() {
        var f = flow(.probing(.retry), mints: 2)
        XCTAssertEqual(f.handle(.probed(.unreachable)), fail(refused, nil))
        XCTAssertEqual(f.state, .failed)
    }

    func testRecheckUnreachableKeepsPage() {
        var f = flow(.probing(.recheck))
        XCTAssertEqual(f.handle(.probed(.unreachable)), [retry])
        XCTAssertEqual(f.state, .failed)
    }

    func testSocketCheckNoDaemonGivesUnitBack() {
        for err in [LoginCodeError.daemonUnreachable, .timeout] {
            var f = flow(.socketCheck, mints: 1)
            XCTAssertEqual(f.handle(.minted(.failure(err))), daemonDownPage)
            XCTAssertEqual(f.state, .daemonDown)
            XCTAssertEqual(f.mintsThisCycle, 0)
        }
    }

    func testSocketCheckHTTPOff() {
        var f = flow(.socketCheck, mints: 1)
        XCTAssertEqual(f.handle(.minted(.failure(.httpOff))), fail("L'accès web de pigtaild est coupé", "pigtaild tourne sans écoute HTTP (--http \"\")"))
        XCTAssertEqual(f.state, .failed)
        XCTAssertEqual(f.mintsThisCycle, 1)
    }

    func testSocketCheckOKLogsInOnNewAddressWithThatCode() {
        var f = flow(.socketCheck, mints: 1)
        XCTAssertEqual(f.handle(ok("127.0.0.1:7811", code: "fresh")), [.saveAddress(newAddr), .loadLogin(newAddr, code: "fresh")])
        XCTAssertEqual(f.state, .loggingIn)
        XCTAssertEqual(f.address, newAddr)
        XCTAssertEqual(f.mintsThisCycle, 1)
    }

    func testSocketCheckRefusals() {
        let cases: [(LoginCodeError, String)] = [(.forbidden, "réponse 403"), (.server(500), "réponse 500"), (.malformed, "réponse illisible")]
        for (err, detail) in cases {
            var f = flow(.socketCheck, mints: 1)
            XCTAssertEqual(f.handle(.minted(.failure(err))), fail(refused, detail))
            XCTAssertEqual(f.state, .failed)
        }
    }

    func testProbeHostRefused() {
        for r in [ProbeReason.launch, .gate, .retry, .recheck, .afterLogin] {
            var f = flow(.probing(r))
            XCTAssertEqual(f.handle(.probed(.hostRefused)), fail(refused, "réponse 403"), "\(r)")
            XCTAssertEqual(f.state, .failed)
        }
    }

    func testProbeUnexpected() {
        var f = flow(.probing(.launch))
        XCTAssertEqual(f.handle(.probed(.unexpected(502))), fail(refused, "réponse 502"))
        XCTAssertEqual(f.state, .failed)
    }

    func testMintingOKLogsInKeepingHost() {
        var f = flow(.minting, mints: 1)
        let lh = DaemonAddress(httpAddr: "localhost:7800")!
        XCTAssertEqual(f.handle(ok("localhost:7800", code: "k")), [.saveAddress(lh), .loadLogin(lh, code: "k")])
        XCTAssertEqual(f.state, .loggingIn)
    }

    func testMintingNoDaemon() {
        var f = flow(.minting, mints: 2)
        XCTAssertEqual(f.handle(.minted(.failure(.timeout))), daemonDownPage)
        XCTAssertEqual(f.state, .daemonDown)
        XCTAssertEqual(f.mintsThisCycle, 1)
    }

    func testMintingHTTPOff() {
        var f = flow(.minting, mints: 1)
        XCTAssertEqual(f.handle(.minted(.failure(.httpOff))), fail(ConnectionFlow.httpOffTitle, ConnectionFlow.httpOffDetail))
    }

    func testMintingRefusals() {
        let cases: [(LoginCodeError, String)] = [(.forbidden, "réponse 403"), (.server(503), "réponse 503"), (.malformed, "réponse illisible")]
        for (err, detail) in cases {
            var f = flow(.minting, mints: 1)
            XCTAssertEqual(f.handle(.minted(.failure(err))), fail(refused, detail))
        }
    }

    func testLoginFinishedProbesAfterLogin() {
        var f = flow(.loggingIn, mints: 1)
        XCTAssertEqual(f.handle(.loginNavigationFinished), [.probe])
        XCTAssertEqual(f.state, .probing(.afterLogin))
    }

    func testLoginNavigationFailedIsDaemonDown() {
        var f = flow(.loggingIn, mints: 1)
        XCTAssertEqual(f.handle(.loginNavigationFailed), daemonDownPage)
        XCTAssertEqual(f.state, .daemonDown)
        XCTAssertEqual(f.mintsThisCycle, 1)
    }

    func testLoginRefusedMintsAgainThenCaps() {
        var f = flow(.loggingIn, mints: 1)
        XCTAssertEqual(f.handle(.loginRefused), [.mint])
        XCTAssertEqual(f.state, .minting)
        var g = flow(.loggingIn, mints: 2)
        XCTAssertEqual(g.handle(.loginRefused), fail(refused, "réponse 410"))
        XCTAssertEqual(g.state, .failed)
    }

    func testRetryFromDaemonDownProbes() {
        var f = flow(.daemonDown, mints: 1)
        XCTAssertEqual(f.handle(.retryFired), [.probe])
        XCTAssertEqual(f.state, .probing(.retry))
    }

    func testRetryFromFailedRechecks() {
        var f = flow(.failed, mints: 1)
        XCTAssertEqual(f.handle(.retryFired), [.probe])
        XCTAssertEqual(f.state, .probing(.recheck))
    }

    // MARK: gate reload bound

    func testGateValidReloadsAtMostTwiceThenDaemonDown() {
        var f = flow(.showing)
        for _ in 0..<2 {
            XCTAssertEqual(f.handle(.sessionRefused), [.probe])
            XCTAssertEqual(f.handle(.probed(.valid)), [.cancelRetry, .loadFront(addr)])
            XCTAssertEqual(f.state, .showing)
        }
        XCTAssertEqual(f.gateReloadsThisCycle, 2)
        XCTAssertEqual(f.handle(.sessionRefused), [.probe])
        XCTAssertEqual(f.handle(.probed(.valid)), daemonDownPage)
        XCTAssertEqual(f.state, .daemonDown)
    }

    func testGateReloadCounterResetsOnReloadAndOnNonGateValid() {
        var f = flow(.showing, reloads: 2)
        _ = f.handle(.reloadRequested)
        XCTAssertEqual(f.gateReloadsThisCycle, 0)
        var g = flow(.daemonDown, reloads: 2)
        _ = g.handle(.reloadRequested)
        XCTAssertEqual(g.gateReloadsThisCycle, 0)
        var h = flow(.probing(.retry), reloads: 2)
        _ = h.handle(.probed(.valid))
        XCTAssertEqual(h.gateReloadsThisCycle, 0)
    }

    func testShowingSessionRefusedOrReloadGates() {
        var f = flow(.showing)
        XCTAssertEqual(f.handle(.sessionRefused), [.probe])
        XCTAssertEqual(f.state, .probing(.gate))
        var g = flow(.showing)
        XCTAssertEqual(g.handle(.reloadRequested), [.probe])
        XCTAssertEqual(g.state, .probing(.gate))
        XCTAssertEqual(g.mintsThisCycle, 0)
    }

    func testReloadFromPagesResetsCounter() {
        for s in [FlowState.daemonDown, .failed] {
            var f = flow(s, mints: 2)
            XCTAssertEqual(f.handle(.reloadRequested), [.probe])
            XCTAssertEqual(f.state, .probing(.launch))
            XCTAssertEqual(f.mintsThisCycle, 0)
        }
    }

    func testBusyStatesIgnoreBursts() {
        for s in [FlowState.probing(.launch), .probing(.afterLogin), .minting, .socketCheck, .loggingIn] {
            for e in [FlowEvent.sessionRefused, .reloadRequested] {
                var f = flow(s, mints: 1)
                XCTAssertEqual(f.handle(e), [], "\(s) \(e)")
                XCTAssertEqual(f.state, s)
                XCTAssertEqual(f.mintsThisCycle, 1)
            }
        }
    }

    // MARK: named properties

    /// A deterministic pseudo-random source (no flake).
    private struct LCG {
        var s: UInt64
        mutating func next(_ n: Int) -> Int {
            s = s &* 6364136223846793005 &+ 1442695040888963407
            return Int((s >> 33) % UInt64(n))
        }
    }

    private let pool: [FlowEvent] = [
        .sessionRefused, .probed(.invalid), .probed(.unreachable), .probed(.hostRefused),
        .minted(.success(LoginCode(code: "c", httpAddr: "127.0.0.1:7800"))),
        .minted(.failure(.daemonUnreachable)), .minted(.failure(.timeout)), .minted(.failure(.httpOff)),
        .minted(.failure(.malformed)), .loginRefused, .loginNavigationFinished, .loginNavigationFailed, .retryFired,
    ]

    func testMintsNeverExceedTwoPerCycleOverAnyEventSequence() {
        for seed in 1...300 {
            var rng = LCG(s: UInt64(seed))
            var f = ConnectionFlow(address: addr)
            var outstanding = 0
            _ = f.handle(.start)
            for _ in 0..<200 {
                let e = pool[rng.next(pool.count)]
                let before = f.state
                if case .minted(.failure(let err)) = e, err == .daemonUnreachable || err == .timeout,
                   before == .minting || before == .socketCheck {
                    outstanding -= 1 // the unit is given back
                }
                let effects = f.handle(e)
                outstanding += effects.filter { $0 == .mint }.count
                if f.state == .showing { outstanding = 0 } // a new cycle
                XCTAssertLessThanOrEqual(outstanding, 2, "seed \(seed) after \(e)")
                XCTAssertEqual(outstanding, f.mintsThisCycle, "seed \(seed) after \(e)")
            }
        }
    }

    func testMalformedReplyShowsUnreadableCause() {
        for s in [FlowState.minting, .socketCheck] {
            var f = flow(s, mints: 1)
            XCTAssertEqual(f.handle(.minted(.failure(.malformed))),
                           [.showDaemonDown(title: "pigtaild refuse la connexion", detail: "réponse illisible"), retry])
            var g = flow(s, mints: 1)
            XCTAssertEqual(g.handle(ok("0.0.0.0:7800")),
                           [.showDaemonDown(title: "pigtaild refuse la connexion", detail: "réponse illisible"), retry])
            XCTAssertEqual(g.address, addr)
        }
    }

    func testRecheckKeepsHttpOffPage() {
        var f = flow(.probing(.retry))
        XCTAssertEqual(f.handle(.probed(.unreachable)), [.mint])
        XCTAssertEqual(f.handle(.minted(.failure(.httpOff))), fail(ConnectionFlow.httpOffTitle, ConnectionFlow.httpOffDetail))
        for _ in 0..<5 {
            XCTAssertEqual(f.handle(.retryFired), [.probe])
            let effects = f.handle(.probed(.unreachable))
            XCTAssertEqual(effects, [retry])
            XCTAssertFalse(effects.contains { if case .showDaemonDown = $0 { return true } else { return false } })
            XCTAssertEqual(f.state, .failed)
        }
    }

    func testRetryFromFailedNeverMints() {
        let outcomes: [ProbeOutcome] = [.invalid, .unreachable, .hostRefused, .unexpected(500), .valid]
        for seed in 1...200 {
            var rng = LCG(s: UInt64(seed))
            var f = flow(.failed, mints: rng.next(3))
            XCTAssertEqual(f.handle(.retryFired), [.probe])
            let o = outcomes[rng.next(outcomes.count)]
            let effects = f.handle(.probed(o))
            XCTAssertFalse(effects.contains(.mint), "\(o)")
            XCTAssertEqual(f.state, o == .valid ? .showing : .failed)
        }
    }

    // MARK: window closed

    func testWindowClosedFromShowingGoesIdleAndCancelsRetry() {
        var f = flow(.showing)
        XCTAssertEqual(f.handle(.windowClosed), [.cancelRetry])
        XCTAssertEqual(f.state, .idle)
    }
    func testWindowClosedFromDaemonDownCancelsTheRetry() {
        var f = flow(.daemonDown)
        XCTAssertEqual(f.handle(.windowClosed), [.cancelRetry])
        XCTAssertEqual(f.state, .idle)
    }
    func testLateResultsAfterCloseAreDropped() {
        var f = flow(.minting)
        _ = f.handle(.windowClosed)
        XCTAssertEqual(f.handle(ok()), [])
        XCTAssertEqual(f.handle(.retryFired), [])
        XCTAssertEqual(f.state, .idle)
    }
    func testStartAfterCloseProbesAgain() {
        var f = flow(.showing)
        _ = f.handle(.windowClosed)
        XCTAssertEqual(f.handle(.start), [.probe])
    }
}
