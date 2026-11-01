import XCTest
@testable import MenuBarCore

/// `GET /servers` has carried `owner` / `owner_alive` since PR #52 and
/// `recent_deaths` since PR #47. The menu bar decoded neither, so the surface
/// watched all day was the least informative one: it could not answer "whose
/// server is this?" or "did one die?" without a trip to the CLI.
///
/// These tests are grouped in one file because the two gaps are one gap — the
/// same wire response, dropped in the same decoder.
final class OwnerDecodingTests: XCTestCase {
    private func makeClient() -> DaemonClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return DaemonClient(baseURL: URL(string: "http://127.0.0.1:7800")!,
                            token: "tok",
                            session: URLSession(configuration: config))
    }

    override func tearDown() {
        StubURLProtocol.handler = nil
        StubURLProtocol.lastRequest = nil
        super.tearDown()
    }

    /// The wire contract's one trap (docs/wire-protocol.md): `owner_alive` is
    /// `omitempty`, so it only ever appears as `true`. Its absence means two
    /// different things, told apart by whether `owner` is there at all —
    /// unclaimed, or claimed by an owner not confirmed alive. A client reading
    /// `owner_alive == false` as "dead" would flag every unclaimed server.
    func testOwnerStateSeparatesUnclaimedFromUnconfirmed() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":{"available_bytes":0,"floor_bytes":0,"servers":[
              {"workspace_id":"ws_a","server":"dev","port":6010,"rss_bytes":0,
               "idle_seconds":0,"owner":"sid:4711@laptop","owner_alive":true},
              {"workspace_id":"ws_b","server":"dev","port":6011,"rss_bytes":0,
               "idle_seconds":0,"owner":"sid:99@laptop"},
              {"workspace_id":"ws_c","server":"dev","port":6012,"rss_bytes":0,
               "idle_seconds":0}]},
             "meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let resp = try await makeClient().servers()

        XCTAssertEqual(resp.servers[0].ownerState, .alive("sid:4711@laptop"))
        XCTAssertEqual(resp.servers[1].ownerState, .unconfirmed("sid:99@laptop"))
        XCTAssertEqual(resp.servers[2].ownerState, .unclaimed)
    }

    func testRecentDeathsDecoded() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":{"available_bytes":0,"floor_bytes":0,"servers":[],
              "recent_deaths":[
                {"workspace_id":"ws_k2m1","server":"dev","port":3016,"ago_seconds":240}]},
             "meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let resp = try await makeClient().servers()
        XCTAssertEqual(resp.recentDeaths.count, 1)
        XCTAssertEqual(resp.recentDeaths[0].workspaceID, "ws_k2m1")
        XCTAssertEqual(resp.recentDeaths[0].server, "dev")
        XCTAssertEqual(resp.recentDeaths[0].port, 3016)
        XCTAssertEqual(resp.recentDeaths[0].agoSeconds, 240)
    }

    /// `recent_deaths` is `omitempty` on the Go side, so "nothing died" arrives
    /// as an absent key. It must decode to an empty list, not to nil: two ways
    /// of spelling the same thing is how a `if let` ends up guarding a section
    /// that should simply be empty.
    func testAbsentRecentDeathsDecodesAsEmptyNotNil() async throws {
        StubURLProtocol.handler = { _ in
            let json = """
            {"data":{"available_bytes":0,"floor_bytes":0,"servers":[]},
             "meta":{"request_id":"r"}}
            """
            return (200, Data(json.utf8))
        }
        let resp = try await makeClient().servers()
        XCTAssertTrue(resp.recentDeaths.isEmpty)
    }
}

final class OwnerAndDeathsViewModelTests: XCTestCase {

    private let now = Date(timeIntervalSince1970: 1_000_000_000)

    private func server(_ ws: String, owner: String?, alive: Bool) -> ServerView {
        ServerView(workspaceID: ws, server: "dev", branch: nil, port: 6010,
                   rssBytes: 0, idleSeconds: 0, pinnedUntil: nil,
                   owner: owner, ownerAlive: alive)
    }

    // MARK: - owner on the row

    func testOwnerLabelMirrorsTheCLI() {
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [
            server("ws_a", owner: "sid:4711@laptop", alive: true),
            server("ws_b", owner: "feat-window", alive: true),
            server("ws_c", owner: nil, alive: false),
        ])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")

        let a = state.runningRows.first { $0.workspaceID == "ws_a" }!
        let b = state.runningRows.first { $0.workspaceID == "ws_b" }!
        let c = state.runningRows.first { $0.workspaceID == "ws_c" }!

        // This machine's host is noise on every line, exactly as in `pigtail servers`.
        XCTAssertEqual(a.ownerLabel, "sid:4711")
        // An opaque identity is printed as-is.
        XCTAssertEqual(b.ownerLabel, "feat-window")
        // Unclaimed shows nothing rather than a dash: a popover row is not a table
        // column, and an empty label simply drops out of the line.
        XCTAssertEqual(c.ownerLabel, "")
    }

    func testAnotherMachinesHostIsKept() {
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [
            server("ws_a", owner: "sid:4711@otherbox", alive: true),
        ])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")
        XCTAssertEqual(state.runningRows[0].ownerLabel, "sid:4711@otherbox")
    }

    /// The dagger the CLI prints. Only an owner that exists but is not confirmed
    /// alive earns it — an unclaimed server has no owner to be dead.
    func testOnlyAnUnconfirmedOwnerIsFlagged() {
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [
            server("ws_a", owner: "sid:1@laptop", alive: true),
            server("ws_b", owner: "sid:2@laptop", alive: false),
            server("ws_c", owner: nil, alive: false),
        ])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")

        XCTAssertFalse(state.runningRows.first { $0.workspaceID == "ws_a" }!.ownerUnconfirmed)
        XCTAssertTrue(state.runningRows.first { $0.workspaceID == "ws_b" }!.ownerUnconfirmed)
        XCTAssertFalse(state.runningRows.first { $0.workspaceID == "ws_c" }!.ownerUnconfirmed,
                       "an unclaimed server has no owner to be dead")
    }

    /// Pinning must not silently change who owns a server. The optimistic copies
    /// rebuild the row field by field, which is exactly where a newly added
    /// field gets dropped.
    func testOptimisticPinCopiesPreserveOwnership() {
        let row = RunningRow(workspaceID: "ws_a", server: "dev", port: 6010, branch: "feat/x",
                             idleLabel: "", pinned: false, pinLabel: "", pinActionLabel: "Pin",
                             rssLabel: "1 Mo", ownerLabel: "sid:7", ownerUnconfirmed: true)

        let pinned = row.pinnedCopy(label: "30m")
        XCTAssertEqual(pinned.ownerLabel, "sid:7")
        XCTAssertTrue(pinned.ownerUnconfirmed)

        let unpinned = pinned.unpinnedCopy()
        XCTAssertEqual(unpinned.ownerLabel, "sid:7")
        XCTAssertTrue(unpinned.ownerUnconfirmed)
    }

    // MARK: - deaths

    func testDeathRowsBuiltFromRecentDeaths() {
        let resp = ServersResponse(
            availableBytes: 0, floorBytes: 0, servers: [],
            recentDeaths: [
                DeadServerView(workspaceID: "ws_k", server: "dev", port: 3016, agoSeconds: 45),
                DeadServerView(workspaceID: "ws_j", server: "sb", port: 3017, agoSeconds: 240),
            ])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")

        XCTAssertEqual(state.deathRows.count, 2)
        // Order is the daemon's (oldest first per the wire contract); not re-sorted.
        XCTAssertEqual(state.deathRows[0].workspaceID, "ws_k")
        XCTAssertEqual(state.deathRows[0].server, "dev")
        XCTAssertEqual(state.deathRows[0].port, 3016)
        // Same rendering as the CLI's formatAgo: whole seconds under a minute,
        // whole minutes above, never a compound "4m12s".
        XCTAssertEqual(state.deathRows[0].agoLabel, "45s")
        XCTAssertEqual(state.deathRows[1].agoLabel, "4m")
    }

    func testNoDeathsMeansNoRows() {
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")
        XCTAssertTrue(state.deathRows.isEmpty)
    }

    /// A daemon that stopped answering says nothing about what died, so the
    /// section must clear rather than keep showing a stale cross.
    func testDaemonDownClearsDeaths() {
        let state = ServerListViewModel.build(servers: nil, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")
        XCTAssertTrue(state.deathRows.isEmpty)
    }

    /// Every optimistic transform rebuilds PopoverState by hand. Stopping a
    /// server must not make the record of an unrelated crash vanish from the
    /// popover until the next poll.
    func testOptimisticTransformsKeepDeaths() {
        let resp = ServersResponse(
            availableBytes: 0, floorBytes: 0,
            servers: [server("ws_a", owner: nil, alive: false)],
            recentDeaths: [DeadServerView(workspaceID: "ws_k", server: "dev",
                                          port: 3016, agoSeconds: 45)])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now,
                                              idlePurgeGraceSeconds: 0, hostName: "laptop")
        XCTAssertEqual(state.deathRows.count, 1)

        XCTAssertEqual(state.removingRunningRow(id: "ws_a/dev").deathRows.count, 1,
                       "stopping one server dropped an unrelated death")
        XCTAssertEqual(state.clearingRunningRows().deathRows.count, 1,
                       "stop-all dropped the deaths")
        XCTAssertEqual(state.withIdlePurgeGraceSeconds(600).deathRows.count, 1)
        XCTAssertEqual(state.updatingRunningRow(id: "ws_a/dev", { $0 }).deathRows.count, 1)
        let item = StartItem(workspaceID: "ws_z", worktree: "wt", name: "dev", port: 7000)
        XCTAssertEqual(state.startingServer(item).deathRows.count, 1)
    }
}
