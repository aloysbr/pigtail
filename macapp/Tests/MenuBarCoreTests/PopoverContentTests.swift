import XCTest
import SwiftUI
@testable import MenuBarCore

@MainActor
final class PopoverContentTests: XCTestCase {
    private typealias F = UsageFixtures
    private let nb = "\u{00A0}"

    // MARK: fixtures

    private func state(servers: Int = 6, rss: String = "2.1 Go", down: Bool = false) -> PopoverState {
        let rows = (0..<servers).map {
            RunningRow(workspaceID: "ws\($0)", server: "dev", port: 6010 + $0, branch: "", idleLabel: "", pinned: false,
                       pinLabel: "", pinActionLabel: "Pin", rssLabel: "300 Mo")
        }
        return PopoverState(daemonDown: down, activeCount: servers, runningRows: rows, startGroups: [],
                            totalRSSLabel: rss, idlePurgeGraceSeconds: 0)
    }
    private let travelOff = LidDisplay.fold(response: LidResponse(sleepDisabled: false), readFailure: nil,
                                           unavailable: false, actionError: nil, isActing: false)

    private func content(_ usage: UsageDisplay, state s: PopoverState? = nil, loaded: Bool = true,
                         lid: LidDisplay? = nil, denied: Bool = false, expanded: Bool = false,
                         captureStatic: Bool = false, needsApproval: Bool = false) -> PopoverContent {
        PopoverContent(state: s ?? state(), loaded: loaded, usage: usage, lid: lid ?? travelOff,
                       lidNotificationsDenied: denied, detailExpanded: .constant(expanded), loginEnabled: .constant(false),
                       loginNeedsApproval: needsApproval, stopAll: {}, quit: {}, onLidAction: { _ in },
                       now: F.now, timeZone: F.utc, captureStatic: captureStatic)
    }
    private var normal: UsageDisplay { F.ready(F.normal) }

    // MARK: footer

    func testFooterLabels() {
        XCTAssertEqual(PopoverContent.openWindowTitle, "Ouvrir Pigtail")
        XCTAssertEqual(PopoverContent.quitTitle, "Quitter Pigtail")
    }

    // MARK: sections

    func testSectionOrder() {
        XCTAssertEqual(PopoverContent.sections(state: state(), loaded: true, usage: normal),
                       [.header, .verdict, .nextSession, .detail, .settings, .footer])
    }

    func testBodyReadsTheSectionList() throws {
        let source = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources/MenuBarCore/PopoverContent.swift")
        let code = try String(contentsOf: source, encoding: .utf8)
        XCTAssertGreaterThan(code.count, 1000, "instrument control: the source was not read")
        XCTAssertTrue(code.contains("ForEach(Self.sections("), "the body draws the very list the test pins")
    }

    func testDaemonDownSections() {
        XCTAssertEqual(PopoverContent.sections(state: state(servers: 0, rss: "", down: true), loaded: true, usage: .loading),
                       [.header, .daemonMessage, .settings, .footer])
    }

    func testNeverRefreshedShowsLoading() async {
        // a fresh store starts at daemonDown: only `loaded` tells that apart from a real outage
        let fresh = PopoverStore(client: NeverCalledClient())
        XCTAssertFalse(fresh.loaded)
        XCTAssertTrue(fresh.state.daemonDown)
        XCTAssertEqual(PopoverContent.sections(state: fresh.state, loaded: fresh.loaded, usage: .loading),
                       [.header, .loading, .settings, .footer])
        XCTAssertEqual(PopoverContent.loadingText, "Lecture des relevés…")
        // after a first failed refresh the outage is real
        await fresh.refresh()
        XCTAssertTrue(fresh.loaded)
        XCTAssertEqual(PopoverContent.sections(state: fresh.state, loaded: fresh.loaded, usage: .loading),
                       [.header, .daemonMessage, .settings, .footer])
    }

    func testUsageUnreachableAndLoadingKeepTheHeaderMetric() {   // G12, G9
        let down = UsageDisplay(state: .unreachable, overview: nil, readFailed: true)
        XCTAssertEqual(PopoverContent.sections(state: state(), loaded: true, usage: down), [.header, .verdict, .settings, .footer])
        XCTAssertEqual(PopoverContent.sections(state: state(), loaded: true, usage: .loading), [.header, .verdict, .settings, .footer])
        XCTAssertTrue(PopoverContent.showsStopAll(state: state(), loaded: true))
    }

    func testNoStopAllWithoutServers() {
        XCTAssertFalse(PopoverContent.showsStopAll(state: state(servers: 0, rss: ""), loaded: true))
        XCTAssertFalse(PopoverContent.showsStopAll(state: state(servers: 2, down: true), loaded: true))
        XCTAssertFalse(PopoverContent.showsStopAll(state: state(servers: 2), loaded: false))
        XCTAssertTrue(PopoverContent.showsStopAll(state: state(servers: 2), loaded: true))
    }

    func testHeaderMetricFrenchDecimal() {
        XCTAssertEqual(PopoverContent.headerMetric(for: state(servers: 6, rss: "2.1 Go")), "6 actifs · 2,1\(nb)Go")
        XCTAssertEqual(PopoverContent.headerMetric(for: state(servers: 6, rss: "512 Mo")), "6 actifs · 512\(nb)Mo")
        XCTAssertEqual(PopoverContent.headerMetric(for: state(servers: 6, rss: "")), "6 actifs")
        XCTAssertEqual(PopoverContent.headerMetric(for: state(servers: 1, rss: "")), "1 actif", "one server is singular")
        XCTAssertEqual(PopoverContent.headerMetric(for: state(servers: 0, rss: "")), "0 actif", "0 is singular in French")
        XCTAssertEqual(ServerListViewModel.formatRSS(UInt64(2.1 * 1_073_741_824)), "2.1 Go", "formatRSS itself is unchanged")
    }

    // MARK: detail

    private final class Box: ObservableObject { @Published var open = false }
    private struct DetailHost: View {
        @ObservedObject var box: Box
        let overview: UsageOverview
        var body: some View {
            AccountDetailSection(overview: overview, now: UsageFixtures.now, expanded: $box.open).frame(width: 320)
        }
    }
    private func settledHeight<V: View>(_ host: NSHostingView<V>) -> CGFloat {
        for _ in 0..<3 {
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        }
        return host.fittingSize.height
    }
    private func mount<V: View>(_ v: V) -> NSHostingView<V> {
        let host = NSHostingView(rootView: v)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 320, height: 900), styleMask: [.borderless],
                              backing: .buffered, defer: false)
        window.contentView = host
        return host
    }

    func testDetailBindingOpens() {
        let box = Box()
        let host = mount(DetailHost(box: box, overview: F.normal))
        let closed = settledHeight(host)
        box.open = true
        XCTAssertGreaterThan(settledHeight(host), closed + 40)
    }

    func testOpenDetailIsNaturalHeight() {
        let box = Box()
        let host = mount(DetailHost(box: box, overview: F.stale))
        let closed = settledHeight(host)
        box.open = true
        let open = settledHeight(host)
        // the reference: the same rows in a plain VStack, header included
        let model = AccountDetailModel(overview: F.stale, now: F.now)
        let reference = mount(VStack(alignment: .leading, spacing: 0) {
            Text("Détail par compte").padding(.vertical, 4)
            ForEach(Array(model.rows.enumerated()), id: \.offset) { AccountRow(model: $0.element) }
        }.frame(width: 320).fixedSize(horizontal: false, vertical: true))
        let natural = settledHeight(reference)
        XCTAssertGreaterThan(open - closed, 160, "four rows, with the stale + failed notice line, exceed the old 160 pt cap")
        XCTAssertEqual(open - closed, natural - 24, accuracy: 40, "open = the rows' natural height, no inner scrolling")
        XCTAssertEqual(model.rows.count, 4)
        XCTAssertEqual(model.rows[1].notices, "relevé il y a 2\(nb)h\(nb)05 · actualisation en échec")
    }

    func testUnreadableCaptionInOpenDetail() {   // G16
        let o = F.overview([F.m, F.j], verdict: F.normalVerdict, unreadable: 1)
        let model = AccountDetailModel(overview: o, now: F.now)
        XCTAssertEqual(model.caption(expanded: true), "Un compte est illisible")
        XCTAssertNil(model.caption(expanded: false), "folded, the header's dot carries it")
        XCTAssertTrue(model.dotVisible(expanded: false))
        XCTAssertNil(AccountDetailModel(overview: F.normal, now: F.now).caption(expanded: true))
    }

    func testDotHiddenWhenDetailOpen() {   // criterion 12
        let model = AccountDetailModel(overview: F.stale, now: F.now)
        XCTAssertTrue(model.dotVisible(expanded: false))
        XCTAssertFalse(model.dotVisible(expanded: true))
        XCTAssertFalse(AccountDetailModel(overview: F.normal, now: F.now).dotVisible(expanded: false), "no reason, no dot")
    }

    func testVerdictSameWithAndWithoutQuickAccount() {   // criterion 13
        let calm = F.acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 69, h5r: "20/10 23:29", left: 0.643, fast: false, ageMin: 5)
        let withFast = F.overview([F.m, F.i, F.j, F.t], verdict: F.normalVerdict)
        let without = F.overview([F.m, calm, F.j, F.t], verdict: F.normalVerdict)
        XCTAssertEqual(VerdictModel(display: F.ready(withFast), now: F.now, timeZone: F.utc),
                       VerdictModel(display: F.ready(without), now: F.now, timeZone: F.utc))
    }

    // MARK: settings wiring, accessibility

    func testLidNotificationsDeniedWired() {
        XCTAssertTrue(content(normal, denied: true).settingsModel.notices.contains(LidFormat.notificationsDenied))
        XCTAssertFalse(content(normal, denied: false).settingsModel.notices.contains(LidFormat.notificationsDenied))
    }

    func testAccessibilityLabels() {
        XCTAssertEqual(AttentionGlyph.label(for: "plafond de crédit atteint"), "attention : plafond de crédit atteint")
        XCTAssertEqual(DetailHeaderModel(overview: F.stale, now: F.now).dotLabel, "un compte demande attention")
        XCTAssertEqual(DetailHeaderModel(overview: F.overview([F.m], verdict: nil, unreadable: 1), now: F.now).dotLabel,
                       "Un compte est illisible")
        XCTAssertEqual(SettingRow.accessibilityLabel(title: "Travel Mode"), "Travel Mode")
        XCTAssertEqual(SettingRow.accessibilityHint(subtitle: "Désactivé"), "Désactivé")
        XCTAssertNil(SettingRow.accessibilityHint(subtitle: nil))
        // the views wire them (the AX tree is empty in a test process: source check)
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources/MenuBarCore")
        let views = (try? String(contentsOf: dir.appendingPathComponent("UsageViews.swift"), encoding: .utf8)) ?? ""
        let settings = (try? String(contentsOf: dir.appendingPathComponent("SettingsSection.swift"), encoding: .utf8)) ?? ""
        XCTAssertTrue(views.contains(".accessibilityLabel(AttentionGlyph.label(for:"))
        XCTAssertTrue(views.contains(".accessibilityLabel(model.header.dotLabel)"))
        XCTAssertTrue(views.contains(".accessibilityHidden(true)"), "bars and ticks are hidden from VoiceOver")
        XCTAssertTrue(settings.contains(".accessibilityLabel(Self.accessibilityLabel(title: title))"))
    }

    // MARK: 320 pt

    private func measure<V: View>(_ v: V) -> CGSize {
        let c = NSHostingController(rootView: v.frame(maxWidth: .infinity, alignment: .topLeading))
        return c.sizeThatFits(in: CGSize(width: 320, height: 10_000))
    }

    /// What `AppDelegate.showPanel` sizes the panel on: the hosting view's `fittingSize`, no proposal.
    /// A long unwrapped line widens it unless the content itself is fixed at 320 pt.
    private func fittingWidth(_ c: PopoverContent) -> CGFloat {
        let host = NSHostingView(rootView: c)
        return host.fittingSize.width
    }

    func testFittingWidthIs320WithLongLines() {
        XCTAssertEqual(fittingWidth(content(F.ready(F.normal))), PopoverContent.width, accuracy: 0.5, "control: plain state")
        XCTAssertEqual(fittingWidth(content(F.ready(F.normal), needsApproval: true)), PopoverContent.width, accuracy: 0.5,
                       "loginNeedsApproval widened the popover")
        XCTAssertEqual(fittingWidth(content(F.ready(F.normal), denied: true)), PopoverContent.width, accuracy: 0.5,
                       "lidNotificationsDenied widened the popover")
    }

    func testEveryStateFits320() {
        let travelOn = LidDisplay.fold(response: LidResponse(sleepDisabled: true, deadline: F.now.addingTimeInterval(7_000),
                                                             guardError: "ioreg illisible"),
                                       readFailure: nil, unavailable: false, actionError: nil, isActing: false)
        let states: [(String, PopoverContent)] = [
            ("normal", content(F.ready(F.normal))),
            ("tight", content(F.ready(F.tight))),
            ("tooFast", content(F.ready(F.tooFast))),
            ("weekStart", content(F.ready(F.weekStart))),
            ("blockedNow", content(F.ready(F.blockedNow), expanded: true, captureStatic: true)),
            ("stale open", content(F.ready(F.stale, failed: true), expanded: true, captureStatic: true)),
            ("unreadable open", content(F.ready(F.overview([F.m, F.j], verdict: F.normalVerdict, picks: [F.persoI], unreadable: 3)), expanded: true, captureStatic: true)),
            ("usage unreachable", content(UsageDisplay(state: .unreachable, overview: nil, readFailed: true))),
            ("loading", content(.loading)),
            ("never refreshed", content(.loading, state: state(servers: 0, rss: "", down: true), loaded: false)),
            ("daemon down", content(.loading, state: state(servers: 0, rss: "", down: true))),
            ("travel on + denied", content(F.ready(F.normal), lid: travelOn, denied: true)),
        ]
        for (name, c) in states {
            let size = measure(c)
            XCTAssertLessThanOrEqual(size.width, 320.5, "\(name) is wider than 320 pt (\(size.width))")
            XCTAssertGreaterThan(size.height, 80, "\(name) rendered empty")
            XCTAssertLessThan(size.height, 900, "\(name) grew past any plausible popover (\(size.height))")
        }
        // positive control: the measure sees content — the open detail (drawn directly: no window, no
        // measuring pass here) is taller than the collapsed one
        XCTAssertGreaterThan(measure(content(F.ready(F.stale), expanded: true, captureStatic: true)).height,
                             measure(content(F.ready(F.stale), expanded: false)).height + 100)
    }
}

private struct NeverCalledClient: DaemonClienting {
    func servers() async throws -> ServersResponse { throw URLError(.cannotConnectToHost) }
    func workspaces() async throws -> [Workspace] { throw URLError(.cannotConnectToHost) }
    func pin(workspace: String, server: String, duration: PinDuration) async throws {}
    func unpin(workspace: String, server: String) async throws {}
    func stop(workspace: String, server: String) async throws {}
    func stopAll() async throws {}
    func purgePolicy() async throws -> PurgePolicy { throw URLError(.cannotConnectToHost) }
    func setPurgePolicy(graceSeconds: Int) async throws -> PurgePolicy { throw URLError(.cannotConnectToHost) }
}
