import XCTest
@testable import MenuBarCore

final class ServerListViewModelTests: XCTestCase {

    // Fixed "now" for deterministic pin-label calculations.
    // Unix epoch 1_000_000_000 = 2001-09-08T21:46:40Z
    private let now = Date(timeIntervalSince1970: 1_000_000_000)

    // MARK: - daemonDown

    func testDaemonDownWhenServersNil() {
        let state = ServerListViewModel.build(servers: nil, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertTrue(state.daemonDown)
        XCTAssertEqual(state.activeCount, 0)
        XCTAssertTrue(state.runningRows.isEmpty)
        XCTAssertTrue(state.startGroups.isEmpty)
    }

    // MARK: - idleLabel

    func testIdleHiddenBelowFiveMinutes() {
        let srv299 = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                                port: 6010, rssBytes: 0, idleSeconds: 299, pinnedUntil: nil)
        let srv300 = ServerView(workspaceID: "ws1", server: "sb", branch: nil,
                                port: 6011, rssBytes: 0, idleSeconds: 300, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [srv299, srv300])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)

        // sorted by (workspaceID, server): dev < sb
        let row299 = state.runningRows.first { $0.server == "dev" }!
        let row300 = state.runningRows.first { $0.server == "sb" }!

        XCTAssertEqual(row299.idleLabel, "")
        XCTAssertEqual(row300.idleLabel, "5m")
    }

    // MARK: - pinned / pinLabel / pinActionLabel

    func testPinnedLabelAndExtendAction() {
        // pinnedUntil = now + 720 seconds → 12 minutes
        let pinnedUntil = Int64(now.timeIntervalSince1970) + 720
        let srvPinned = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                                   port: 6010, rssBytes: 0, idleSeconds: 0,
                                   pinnedUntil: pinnedUntil)
        let srvUnpinned = ServerView(workspaceID: "ws1", server: "sb", branch: nil,
                                     port: 6011, rssBytes: 0, idleSeconds: 0,
                                     pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0,
                                   servers: [srvPinned, srvUnpinned])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)

        let pinned = state.runningRows.first { $0.server == "dev" }!
        let unpinned = state.runningRows.first { $0.server == "sb" }!

        XCTAssertTrue(pinned.pinned)
        XCTAssertEqual(pinned.pinLabel, "12m")
        XCTAssertEqual(pinned.pinActionLabel, "Extend")

        XCTAssertFalse(unpinned.pinned)
        XCTAssertEqual(unpinned.pinLabel, "")
        XCTAssertEqual(unpinned.pinActionLabel, "Pin")
    }

    // MARK: - runningRows sort

    func testRunningRowsSortedByWorkspaceThenServer() {
        let servers = [
            ServerView(workspaceID: "ws_b", server: "dev",      branch: nil, port: 6020, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil),
            ServerView(workspaceID: "ws_a", server: "storybook", branch: nil, port: 6011, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil),
            ServerView(workspaceID: "ws_a", server: "dev",      branch: nil, port: 6010, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil),
            ServerView(workspaceID: "ws_b", server: "api",      branch: nil, port: 6021, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil),
        ]
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: servers)
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)

        XCTAssertEqual(state.runningRows.map { $0.workspaceID + "/" + $0.server },
                       ["ws_a/dev", "ws_a/storybook", "ws_b/api", "ws_b/dev"])
    }

    // MARK: - branch

    func testBranchFallsBackToEmptyString() {
        let srv = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                             port: 6010, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [srv])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertEqual(state.runningRows[0].branch, "")
    }

    // MARK: - startGroups: exclude running servers

    func testStartGroupsExcludeRunningServers() {
        let ws = Workspace(id: "ws1",
                           path: "/repo/feat-auth",
                           ports: ["dev": 6010, "storybook": 6011],
                           lastActivityAt: now,
                           displayRef: nil)
        // "dev" is already running
        let running = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                                 port: 6010, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [running])
        let state = ServerListViewModel.build(servers: resp, workspaces: [ws], now: now, idlePurgeGraceSeconds: 0)

        XCTAssertEqual(state.startGroups.count, 1)
        let group = state.startGroups[0]
        XCTAssertEqual(group.workspaceID, "ws1")
        XCTAssertEqual(group.items.count, 1)
        XCTAssertEqual(group.items[0].name, "storybook")
    }

    // MARK: - startGroups: sort by lastActivityAt desc; drop fully-running ws

    func testStartGroupsSortedMostRecentFirst() {
        let older = Workspace(id: "ws_older",
                              path: "/repo/old",
                              ports: ["dev": 7010, "sb": 7011],
                              lastActivityAt: Date(timeIntervalSince1970: 1_000_000_000 - 3600),
                              displayRef: nil)
        let newer = Workspace(id: "ws_newer",
                              path: "/repo/new",
                              ports: ["dev": 8010],
                              lastActivityAt: Date(timeIntervalSince1970: 1_000_000_000 - 60),
                              displayRef: nil)
        // ws_newer/dev is running → group has no items → omitted
        let running = ServerView(workspaceID: "ws_newer", server: "dev", branch: nil,
                                 port: 8010, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [running])
        let state = ServerListViewModel.build(servers: resp, workspaces: [older, newer], now: now, idlePurgeGraceSeconds: 0)

        // ws_newer dropped (all running). ws_older remains.
        XCTAssertEqual(state.startGroups.count, 1)
        XCTAssertEqual(state.startGroups[0].workspaceID, "ws_older")
    }

    func testStartGroupsOrderWhenBothHaveItems() {
        let older = Workspace(id: "ws_older",
                              path: "/repo/old",
                              ports: ["dev": 7010],
                              lastActivityAt: Date(timeIntervalSince1970: 1_000_000_000 - 3600),
                              displayRef: nil)
        let newer = Workspace(id: "ws_newer",
                              path: "/repo/new",
                              ports: ["dev": 8010],
                              lastActivityAt: Date(timeIntervalSince1970: 1_000_000_000 - 60),
                              displayRef: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let state = ServerListViewModel.build(servers: resp, workspaces: [older, newer], now: now, idlePurgeGraceSeconds: 0)

        // newer first (more recent lastActivityAt)
        XCTAssertEqual(state.startGroups.map { $0.workspaceID }, ["ws_newer", "ws_older"])
    }

    // MARK: - worktree is basename of path

    func testWorktreeIsBasenameOfPath() {
        let ws = Workspace(id: "ws1",
                           path: "/Users/x/repo/feat-auth",
                           ports: ["dev": 6010],
                           lastActivityAt: now,
                           displayRef: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let state = ServerListViewModel.build(servers: resp, workspaces: [ws], now: now, idlePurgeGraceSeconds: 0)

        XCTAssertEqual(state.startGroups[0].worktree, "feat-auth")
    }

    // MARK: - shortDur

    func testShortDurFormats() {
        XCTAssertEqual(ServerListViewModel.shortDur(0),    "<1m")
        XCTAssertEqual(ServerListViewModel.shortDur(45),   "<1m")
        XCTAssertEqual(ServerListViewModel.shortDur(59),   "<1m")
        XCTAssertEqual(ServerListViewModel.shortDur(60),   "1m")
        XCTAssertEqual(ServerListViewModel.shortDur(660),  "11m")
        XCTAssertEqual(ServerListViewModel.shortDur(3599), "59m")
        XCTAssertEqual(ServerListViewModel.shortDur(3600), "1h0m")
        XCTAssertEqual(ServerListViewModel.shortDur(3780), "1h3m")
        XCTAssertEqual(ServerListViewModel.shortDur(7323), "2h2m")
    }

    // MARK: - formatRSS

    func testFormatRSSUnder1GiB() {
        // 421_527_552 bytes = 402 MiB
        XCTAssertEqual(ServerListViewModel.formatRSS(421_527_552), "402 Mo")
    }

    func testFormatRSSExactly1GiB() {
        // 1_073_741_824 bytes = 1024 MiB = 1.0 GiB
        XCTAssertEqual(ServerListViewModel.formatRSS(1_073_741_824), "1.0 Go")
    }

    func testFormatRSSOver1GiB() {
        // 1_610_612_736 bytes = 1536 MiB = 1.5 GiB
        XCTAssertEqual(ServerListViewModel.formatRSS(1_610_612_736), "1.5 Go")
    }

    func testFormatRSSZero() {
        XCTAssertEqual(ServerListViewModel.formatRSS(0), "0 Mo")
    }

    // MARK: - rssLabel on RunningRow

    func testRSSLabelOnRow() {
        // 421_527_552 bytes = 402 MiB
        let srv = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                             port: 6010, rssBytes: 421_527_552, idleSeconds: 0, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [srv])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertEqual(state.runningRows[0].rssLabel, "402 Mo")
    }

    // MARK: - totalRSSLabel on PopoverState

    func testTotalRSSLabelSumsRunningServers() {
        // 421_527_552 + 1_189_085_184 = 1_610_612_736 bytes = 1.5 GiB
        let srv1 = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                              port: 6010, rssBytes: 421_527_552, idleSeconds: 0, pinnedUntil: nil)
        let srv2 = ServerView(workspaceID: "ws1", server: "sb", branch: nil,
                              port: 6011, rssBytes: 1_189_085_184, idleSeconds: 0, pinnedUntil: nil)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [srv1, srv2])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertEqual(state.totalRSSLabel, "1.5 Go")
    }

    func testTotalRSSLabelEmptyWhenDaemonDown() {
        let state = ServerListViewModel.build(servers: nil, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertEqual(state.totalRSSLabel, "")
    }

    func testTotalRSSLabelEmptyWhenNoServers() {
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        XCTAssertEqual(state.totalRSSLabel, "")
    }

    // MARK: - idlePurgeGraceSeconds

    func testBuildCarriesIdlePurgeGrace() {
        let servers = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let state = ServerListViewModel.build(
            servers: servers, workspaces: [], now: Date(), idlePurgeGraceSeconds: 1800)
        XCTAssertEqual(state.idlePurgeGraceSeconds, 1800)
    }

    func testBuildDaemonDownGraceIsZero() {
        let state = ServerListViewModel.build(
            servers: nil, workspaces: nil, now: Date(), idlePurgeGraceSeconds: 1800)
        XCTAssertTrue(state.daemonDown)
        XCTAssertEqual(state.idlePurgeGraceSeconds, 0)
    }

    func testWithIdlePurgeGraceSecondsReplacesOnlyGrace() {
        let servers = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [])
        let base = ServerListViewModel.build(
            servers: servers, workspaces: [], now: Date(), idlePurgeGraceSeconds: 600)
        let updated = base.withIdlePurgeGraceSeconds(1800)
        XCTAssertEqual(updated.idlePurgeGraceSeconds, 1800)
        // Everything else is carried over unchanged (optimistic copy).
        XCTAssertEqual(updated.daemonDown, base.daemonDown)
        XCTAssertEqual(updated.runningRows, base.runningRows)
        XCTAssertEqual(updated.startGroups, base.startGroups)
        XCTAssertEqual(updated.totalRSSLabel, base.totalRSSLabel)
    }

    // MARK: - Optimistic transforms

    private func twoServerState() -> PopoverState {
        let servers = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [
            ServerView(workspaceID: "ws_a", server: "dev", branch: "feat/x",
                       port: 6010, rssBytes: 1 << 20, idleSeconds: 0, pinnedUntil: nil),
            ServerView(workspaceID: "ws_b", server: "dev", branch: nil,
                       port: 6011, rssBytes: 2 << 20, idleSeconds: 0, pinnedUntil: nil),
        ])
        return ServerListViewModel.build(
            servers: servers, workspaces: [], now: Date(), idlePurgeGraceSeconds: 0)
    }

    func testRemovingRunningRow() {
        let s = twoServerState()
        XCTAssertEqual(s.runningRows.count, 2)
        let after = s.removingRunningRow(id: "ws_a/dev")
        XCTAssertEqual(after.runningRows.map(\.id), ["ws_b/dev"])
        XCTAssertEqual(after.activeCount, 1)
    }

    func testClearingRunningRows() {
        let after = twoServerState().clearingRunningRows()
        XCTAssertTrue(after.runningRows.isEmpty)
        XCTAssertEqual(after.activeCount, 0)
        XCTAssertEqual(after.totalRSSLabel, "")
    }

    func testUpdatingRunningRowPinThenUnpin() {
        let pinned = twoServerState().updatingRunningRow(id: "ws_a/dev") {
            $0.pinnedCopy(label: PinDuration.h1.badgeLabel)
        }
        let pinnedRow = pinned.runningRows.first { $0.id == "ws_a/dev" }!
        XCTAssertTrue(pinnedRow.pinned)
        XCTAssertEqual(pinnedRow.pinLabel, "1h0m")
        XCTAssertEqual(pinnedRow.pinActionLabel, "Extend")
        // Sibling row untouched.
        XCTAssertFalse(pinned.runningRows.first { $0.id == "ws_b/dev" }!.pinned)

        let unpinned = pinned.updatingRunningRow(id: "ws_a/dev") { $0.unpinnedCopy() }
        let unpinnedRow = unpinned.runningRows.first { $0.id == "ws_a/dev" }!
        XCTAssertFalse(unpinnedRow.pinned)
        XCTAssertEqual(unpinnedRow.pinLabel, "")
        XCTAssertEqual(unpinnedRow.pinActionLabel, "Pin")
    }

    func testPinDurationSeconds() {
        XCTAssertEqual(PinDuration.m30.seconds, 1800)
        XCTAssertEqual(PinDuration.h1.seconds, 3600)
        XCTAssertEqual(PinDuration.h4.seconds, 14400)
    }

    func testStartingServerAddsPendingRowAndRemovesStartItem() {
        let servers = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [
            ServerView(workspaceID: "ws_a", server: "dev", branch: nil,
                       port: 6010, rssBytes: 0, idleSeconds: 0, pinnedUntil: nil),
        ])
        let ws = Workspace(id: "ws_a", path: "/repo/feat-a",
                           ports: ["dev": 6010, "sb": 6011],
                           lastActivityAt: Date(), displayRef: "feat/a")
        let state = ServerListViewModel.build(
            servers: servers, workspaces: [ws], now: Date(), idlePurgeGraceSeconds: 0)

        // Precondition: "sb" is startable (dev is already running).
        let sbItem = state.startGroups.flatMap(\.items).first { $0.name == "sb" }!
        XCTAssertEqual(sbItem.id, "ws_a:sb")

        let after = state.startingServer(sbItem)

        // A pending row appears, id-matching the eventual real row, carrying the port.
        let pending = after.runningRows.first { $0.id == "ws_a/sb" }
        XCTAssertNotNil(pending)
        XCTAssertTrue(pending!.pending)
        XCTAssertEqual(pending!.port, 6011)
        // The item left the Start list (its group is now empty → dropped).
        XCTAssertFalse(after.startGroups.flatMap(\.items).contains { $0.name == "sb" })
        // The already-running row is untouched and not pending.
        XCTAssertFalse(after.runningRows.first { $0.id == "ws_a/dev" }!.pending)
    }

    func testDurablePinShowsInfinityBadge() {
        let srv = ServerView(workspaceID: "ws1", server: "dev", branch: nil,
                             port: 6010, rssBytes: 0, idleSeconds: 0,
                             pinnedUntil: nil, pinnedDurable: true)
        let resp = ServersResponse(availableBytes: 0, floorBytes: 0, servers: [srv])
        let state = ServerListViewModel.build(servers: resp, workspaces: [], now: now, idlePurgeGraceSeconds: 0)
        let row = state.runningRows.first!
        XCTAssertTrue(row.pinned)
        XCTAssertEqual(row.pinLabel, "∞")
        // Picking 30 min / 1 h / 4 h on a durable pin replaces it (the last
        // choice wins), so the action does not read "Extend".
        XCTAssertEqual(row.pinActionLabel, "Change")
    }

    func testOptimisticDurablePinSaysChangeTimedSaysExtend() {
        let row = RunningRow(workspaceID: "ws1", server: "dev", port: 6010, branch: "",
                             idleLabel: "", pinned: false, pinLabel: "", pinActionLabel: "Pin",
                             rssLabel: "", pending: false, ownerLabel: "", ownerUnconfirmed: false)
        XCTAssertEqual(row.pinnedCopy(label: PinDuration.untilUnpinned.badgeLabel, durable: true).pinActionLabel, "Change")
        XCTAssertEqual(row.pinnedCopy(label: PinDuration.h1.badgeLabel).pinActionLabel, "Extend")
    }

    func testPinDurationBadgeLabels() {
        XCTAssertEqual(PinDuration.m30.badgeLabel, "30m")
        XCTAssertEqual(PinDuration.h4.badgeLabel, "4h0m")
        XCTAssertEqual(PinDuration.untilUnpinned.badgeLabel, "∞")
        XCTAssertNil(PinDuration.untilUnpinned.seconds)
    }
}
