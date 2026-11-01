import XCTest
import SwiftUI
@testable import MenuBarCore

/// The verify captures of the popover's surfaces (spec § Surfaces 2–15 plus the I13/G16 variants),
/// light and dark, 320 pt. Nothing is opened, no focus taken: `ImageRenderer`. Writes the PNGs when
/// `USAGE_CAPTURE_DIR` is set, otherwise skips. `ImageRenderer` draws neither a `ScrollView` nor
/// (reliably) an AppKit `Toggle`: the detail is drawn by the « captures only » branch
/// (`captureStatic`), and a switch is judged on the real popover.
@MainActor
final class UsageCaptureTests: XCTestCase {
    private typealias F = UsageFixtures

    private func state(servers: Int = 6, rss: String = "2.1 Go", down: Bool = false) -> PopoverState {
        let rows = (0..<servers).map {
            RunningRow(workspaceID: "ws\($0)", server: "dev", port: 6010 + $0, branch: "", idleLabel: "", pinned: false,
                       pinLabel: "", pinActionLabel: "Pin", rssLabel: "300 Mo")
        }
        return PopoverState(daemonDown: down, activeCount: servers, runningRows: rows, startGroups: [],
                            totalRSSLabel: rss, idlePurgeGraceSeconds: 0)
    }
    private func lid(on: Bool, noTimer: Bool = false) -> LidDisplay {
        .fold(response: LidResponse(sleepDisabled: on, deadline: on && !noTimer ? F.now.addingTimeInterval(139 * 60) : nil),
              readFailure: nil, unavailable: false, actionError: nil, isActing: false)
    }

    private struct Surface {
        let name: String
        let usage: UsageDisplay
        var state: PopoverState
        var loaded = true
        var lid: LidDisplay
        var expanded = false
        var denied = false
    }

    private func surfaces() -> [Surface] {
        let off = lid(on: false)
        func s(_ name: String, _ o: UsageOverview, expanded: Bool = false, lid l: LidDisplay? = nil) -> Surface {
            Surface(name: name, usage: F.ready(o), state: state(), lid: l ?? off, expanded: expanded)
        }
        let oneNoPicks = F.overview([F.m, F.j], verdict: F.mjVerdict,
                                    picks: [F.persoJ, UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-m")])
        let unreadable = F.overview([F.m, F.j], verdict: F.mjVerdict, picks: [F.persoJ,
                                    UsageScopePick(scope: "globex", display: "gx", master: false, accountUUID: "u-m")], unreadable: 1)
        // « Relevés périmés »-like: one letter = one account, order m, i, j, t (viewer.js:86); i is stale and
        // failed to refresh, j is saturated on 5 h.
        let allRows = F.overview([F.m,
                                  F.acct("u-i", "i", d7: 64, d7r: "25/10 08:59", h5: 69, h5r: "20/10 23:29", left: 0.643, fast: true,
                                         ageMin: 125, failed: true),
                                  F.acct("u-j", "j", d7: 2, d7r: "27/10 02:59", h5: 91, h5r: "20/10 21:52", left: 0.9, ageMin: 6, o5: true),
                                  F.t], verdict: F.stale.verdict, picks: [F.persoI, F.workT])   // scenario 5's verdict: i's 125 min
        return [
            s("02-header", F.normal),
            Surface(name: "02-header-no-server", usage: F.ready(F.normal), state: state(servers: 0, rss: ""), lid: off),
            s("03-verdict-holds", F.normal, expanded: true),
            s("04-verdict-tight", F.tight),
            s("05-verdict-later", F.tooFast, expanded: true),
            s("06-verdict-later-provisional", F.weekStart),
            s("07-verdict-blocked-now", F.blockedNow),
            s("08-verdict-stale", F.stale, expanded: true),
            Surface(name: "09-daemon-down", usage: .loading, state: state(servers: 0, rss: "", down: true), lid: off),
            s("10-next-session-plain", oneNoPicks),
            s("10-next-session-triangle", F.normal),
            s("11-detail-collapsed", F.normal),
            s("11-detail-collapsed-dot", F.stale),
            s("11-detail-collapsed-unreadable", unreadable),
            s("12-detail-open", allRows, expanded: true),
            s("12-detail-open-unreadable", unreadable, expanded: true),
            s("13-settings-travel-off", F.normal),
            s("13-settings-travel-on", F.normal, lid: lid(on: true)),
            Surface(name: "13-settings-notifications-denied", usage: F.ready(F.normal), state: state(), lid: off, denied: true),
            s("13-settings-travel-on-no-timer", F.normal, lid: lid(on: true, noTimer: true)),
            Surface(name: "13-settings-down", usage: .loading, state: state(servers: 0, rss: "", down: true), lid: off),
            Surface(name: "15-loading", usage: .loading, state: state(servers: 0, rss: "", down: true), loaded: false, lid: off),
            Surface(name: "15-usage-unreachable", usage: UsageDisplay(state: .unreachable, overview: nil, readFailed: true),
                    state: state(), lid: off),
        ]
    }

    private func render(_ s: Surface, scheme: ColorScheme) throws -> NSImage {
        let view = PopoverContent(state: s.state, loaded: s.loaded, usage: s.usage, lid: s.lid, lidNotificationsDenied: s.denied,
                                  detailExpanded: .constant(s.expanded), loginEnabled: .constant(true), loginNeedsApproval: false,
                                  stopAll: {}, quit: {}, onLidAction: { _ in }, now: F.now, timeZone: F.utc, captureStatic: true)
            .frame(width: PopoverContent.width)
            .background(Color(nsColor: .windowBackgroundColor))
            .environment(\.colorScheme, scheme)
        let r = ImageRenderer(content: view)
        r.scale = 2
        return try XCTUnwrap(r.nsImage)
    }

    func testCapturesEverySurface() throws {
        guard let dir = ProcessInfo.processInfo.environment["USAGE_CAPTURE_DIR"] else {
            throw XCTSkip("USAGE_CAPTURE_DIR not set")
        }
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        for s in surfaces() {
            for scheme in [ColorScheme.light, .dark] {
                let img = try render(s, scheme: scheme)
                XCTAssertGreaterThan(img.size.height, 40, "\(s.name) rendered empty")
                XCTAssertEqual(img.size.width, PopoverContent.width, accuracy: 0.5)
                let png = try XCTUnwrap(img.tiffRepresentation.flatMap { NSBitmapImageRep(data: $0) }?
                    .representation(using: .png, properties: [:]))
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(s.name)-\(scheme == .dark ? "dark" : "light").png"))
            }
        }
    }

    /// Surface 1: the real status item, as `AppDelegate.renderStatusItem(lid:)` builds it (production
    /// `StatusItemImage.base()` + `LidStatusTitle.title`, no badge), on a plausible menu bar, ×4.
    private func menuBar(lid l: LidDisplay, scheme: ColorScheme) throws -> NSImage {
        let title = LidStatusTitle.title(for: l, now: F.now)
        let ink: Color = scheme == .dark ? .white : .black
        let bar = scheme == .dark ? Color(white: 0.16) : Color(white: 0.90)
        let base = StatusItemImage.base()
        let view = HStack(spacing: 4) {
            Image(nsImage: base).renderingMode(.template)
                .frame(width: base.size.width, height: base.size.height)
            if let title { Text(title).font(.system(size: 13)) }
        }
        .foregroundStyle(ink)
        .padding(.horizontal, 8)
        .frame(height: 24)
        .background(bar)
        .environment(\.colorScheme, scheme)
        let r = ImageRenderer(content: view)
        r.scale = 4
        return try XCTUnwrap(r.nsImage)
    }

    func testCapturesTheMenuBarIcon() throws {
        guard let dir = ProcessInfo.processInfo.environment["USAGE_CAPTURE_DIR"] else {
            throw XCTSkip("USAGE_CAPTURE_DIR not set")
        }
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        for (name, l) in [("off", lid(on: false)), ("travel", lid(on: true))] {
            XCTAssertEqual(LidStatusTitle.title(for: l, now: F.now) == nil, name == "off")
            for scheme in [ColorScheme.light, .dark] {
                let img = try menuBar(lid: l, scheme: scheme)
                let png = try XCTUnwrap(img.tiffRepresentation.flatMap { NSBitmapImageRep(data: $0) }?
                    .representation(using: .png, properties: [:]))
                try png.write(to: URL(fileURLWithPath: dir)
                    .appendingPathComponent("01-menubar-icon-\(name)-\(scheme == .dark ? "dark" : "light").png"))
            }
        }
    }

    /// Verify r2 N1: the open-detail capture's verdict agrees with its rows. The daemon takes the oldest
    /// reading of the accounts in the verdict (internal/daemon/usage_overview.go), here i's 125 min.
    func testOpenDetailCaptureVerdictIsStaleLikeItsRows() throws {
        let s = try XCTUnwrap(surfaces().first { $0.name == "12-detail-open" })
        let v = VerdictModel(display: s.usage, now: F.now, timeZone: F.utc)
        XCTAssertEqual(v.tone, .muted)
        XCTAssertEqual(v.notes.map(\.text), ["relevé il y a 2\u{00A0}h\u{00A0}05"])
    }

    /// Without the environment variable the rendering path still runs once (no file written).
    func testRendersWithoutWritingAnything() throws {
        let s = try XCTUnwrap(surfaces().first { $0.name == "12-detail-open" })
        let open = try render(s, scheme: .light)
        var closed = s; closed.expanded = false
        XCTAssertGreaterThan(open.size.height, try render(closed, scheme: .light).size.height + 100,
                             "the open detail is really drawn by the captures-only branch")
    }
}
