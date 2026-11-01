import XCTest
@testable import MenuBarCore

private final class Box: @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [(URL, [String], [String: String])] = []
    private var _conts: [CheckedContinuation<Void, Never>] = []
    var calls: [(URL, [String], [String: String])] { lock.lock(); defer { lock.unlock() }; return _calls }
    var hasWaiter: Bool { lock.lock(); defer { lock.unlock() }; return !_conts.isEmpty }
    func record(_ u: URL, _ a: [String], _ e: [String: String]) { lock.lock(); _calls.append((u, a, e)); lock.unlock() }
    func park(_ c: CheckedContinuation<Void, Never>) { lock.lock(); _conts.append(c); lock.unlock() }
    /// Releases every parked runner, so a failed guard never freezes the suite.
    func releaseAll() { lock.lock(); let cs = _conts; _conts = []; lock.unlock(); cs.forEach { $0.resume() } }
}

@MainActor
final class MaintenanceMonitorTests: XCTestCase {
    private var dir: URL!

    override func setUpWithError() throws {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("maint-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }
    override func tearDownWithError() throws { try? FileManager.default.removeItem(at: dir) }

    private var windowURL: URL { dir.appendingPathComponent("window.json") }

    private func monitor(now: Double = 1792663260, runner: @escaping MaintenanceMonitor.TakeBackRunner = { _, _, _ in .init(status: 0, output: "") }) -> MaintenanceMonitor {
        MaintenanceMonitor(windowURL: windowURL, takeBackCommand: .init(executable: URL(fileURLWithPath: "/fake/take-back"), arguments: ["--back", "--by", "chip"]),
                           now: { Date(timeIntervalSince1970: now) }, runner: runner)
    }

    func testMissingFileIsHidden() {
        let m = monitor()
        m.poll()
        XCTAssertEqual(m.display, .hidden)
    }

    func testUnreadableFileIsFailed() throws {
        try Data("{ truncated".utf8).write(to: windowURL)
        let m = monitor()
        m.poll()
        XCTAssertEqual(m.display, .failed("état de la fenêtre illisible", warnings: []))
    }

    func testReadsRunningThenHiddenWhenFileGoes() throws {
        try MaintenanceFixtures.data("running").write(to: windowURL)
        let m = monitor()
        m.poll()
        guard case .running = m.display else { return XCTFail("running expected, got \(m.display)") }
        try FileManager.default.removeItem(at: windowURL)
        m.poll()
        XCTAssertEqual(m.display, .hidden)
    }

    func testMonitorNeverWrites() throws {
        try MaintenanceFixtures.data("running").write(to: windowURL)
        let before = try FileManager.default.attributesOfItem(atPath: windowURL.path)[.modificationDate] as? Date
        let bytes = try Data(contentsOf: windowURL)
        let m = monitor()
        for _ in 0..<5 { m.poll() }
        XCTAssertEqual(try Data(contentsOf: windowURL), bytes)
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: windowURL.path)[.modificationDate] as? Date, before)
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), ["window.json"])
    }

    func testTakeBackEnvHasNoSessionId() {
        let env = MaintenanceMonitor.takeBackEnvironment(from: ["CLAUDE_CODE_SESSION_ID": "abc", "HOME": "/h", "PATH": "/bin"])
        XCTAssertNil(env["CLAUDE_CODE_SESSION_ID"])
        XCTAssertEqual(env["HOME"], "/h")
        XCTAssertEqual(env["PATH"], "/bin")
    }

    func testTakeBackRunsTheConfiguredCommand() async {
        let box = Box()
        let m = monitor(runner: { u, a, e in box.record(u, a, e); return .init(status: 0, output: "") })
        await m.takeBack()
        XCTAssertEqual(box.calls.count, 1)
        XCTAssertEqual(box.calls[0].0.lastPathComponent, "take-back")
        XCTAssertEqual(box.calls[0].1, ["--back", "--by", "chip"])
        XCTAssertNil(box.calls[0].2["CLAUDE_CODE_SESSION_ID"])
    }

    /// No command set: « Reprendre » runs nothing and says so on the chip.
    func testTakeBackWithoutCommandRunsNothing() async throws {
        try MaintenanceFixtures.data("running").write(to: windowURL)
        let box = Box()
        let m = MaintenanceMonitor(windowURL: windowURL, takeBackCommand: nil,
                                   now: { Date(timeIntervalSince1970: 1792663260) },
                                   runner: { u, a, e in box.record(u, a, e); return .init(status: 0, output: "") })
        await m.takeBack()
        XCTAssertEqual(box.calls.count, 0)
        XCTAssertFalse(m.isTakingBack)
        XCTAssertEqual(m.display, .failed(MaintenanceMonitor.noTakeBackCommandLine, warnings: []))
    }

    /// The command is read from the defaults: unset → nil; set → path (`~` expanded) and arguments.
    func testConfiguredTakeBackCommandReadsDefaults() throws {
        let suite = "maint-defaults-\(UUID().uuidString)"
        let d = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { d.removePersistentDomain(forName: suite) }
        XCTAssertNil(MaintenanceMonitor.configuredTakeBackCommand(defaults: d))
        d.set([String](), forKey: MaintenanceMonitor.takeBackCommandKey)
        XCTAssertNil(MaintenanceMonitor.configuredTakeBackCommand(defaults: d))
        d.set(["/opt/bin/release", ""], forKey: MaintenanceMonitor.takeBackCommandKey)
        XCTAssertNil(MaintenanceMonitor.configuredTakeBackCommand(defaults: d), "an empty argument is refused")
        d.set(["~/bin/release", "--back"], forKey: MaintenanceMonitor.takeBackCommandKey)
        let c = try XCTUnwrap(MaintenanceMonitor.configuredTakeBackCommand(defaults: d))
        XCTAssertEqual(c.executable.path, FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("bin/release").path)
        XCTAssertEqual(c.arguments, ["--back"])
    }

    func testDoubleClickRunsOnce() async {
        let box = Box()
        let m = monitor(runner: { u, a, e in
            box.record(u, a, e)
            await withCheckedContinuation { box.park($0) }
            return .init(status: 0, output: "")
        })
        let first = Task { await m.takeBack() }
        for _ in 0..<10_000 where !box.hasWaiter { await Task.yield() }
        XCTAssertTrue(box.hasWaiter, "instrument control: the first click never reached the runner")
        XCTAssertTrue(m.isTakingBack)
        // The second click while the first runs must return at once; bounded, so a lost guard fails instead of freezing.
        let second = Task { await m.takeBack() }
        var runs = 0
        let returned = await withTaskGroup(of: Bool.self) { g in
            g.addTask { await second.value; return true }
            g.addTask { try? await Task.sleep(for: .seconds(2)); return false }
            let r = await g.next() ?? false
            runs = box.calls.count
            box.releaseAll()   // frees a second take-back that a lost guard would have parked, so the group can end
            g.cancelAll()
            return r
        }
        box.releaseAll()
        await first.value
        await second.value
        XCTAssertTrue(returned, "the second click blocked: the double-click guard is gone")
        XCTAssertEqual(runs, 1, "the second click started a second take-back")
        XCTAssertFalse(m.isTakingBack)
        XCTAssertEqual(box.calls.count, 1)
    }

    func testTakeBackErrorShownUntilNextClick() async throws {
        try MaintenanceFixtures.data("running").write(to: windowURL)
        let box = Box()
        let m = monitor(runner: { u, a, e in
            box.record(u, a, e)
            return box.calls.count == 1 ? .init(status: 1, output: "step one\n\nAutoRaise non rétabli\n") : .init(status: 0, output: "")
        })
        await m.takeBack()
        XCTAssertEqual(m.display, .failed("AutoRaise non rétabli", warnings: []))
        m.poll()   // a refresh keeps it
        XCTAssertEqual(m.display, .failed("AutoRaise non rétabli", warnings: []))
        await m.takeBack()   // the next click clears it
        guard case .running = m.display else { return XCTFail("error must clear on the next click, got \(m.display)") }
    }

    func testTakeBackFailureWithoutOutputNamesTheCode() async {
        let m = monitor(runner: { _, _, _ in .init(status: 3, output: "  \n") })
        await m.takeBack()
        XCTAssertEqual(m.display, .failed("Reprendre a échoué (code 3)", warnings: []))
    }

    /// A take-back command's output on exit 3: the take-back line, then the window's cause LAST.
    nonisolated static let realExit3Output = """
        Mac taken back from proj · A — its next Mac-driving tool will be refused; its job has ended
        maintenance window closed — not restored: AutoRaise
        """

    func testTakeBackCauseTranslatesTheExit3Output() async {
        let m = monitor(runner: { _, _, _ in .init(status: 3, output: Self.realExit3Output) })
        await m.takeBack()
        XCTAssertEqual(m.display, .failed("Fenêtre fermée, non rétabli : AutoRaise", warnings: []))
    }

    func testTakeBackCauseTranslatesStillNotRestored() {
        XCTAssertEqual(MaintenanceMonitor.takeBackFailureLine(status: 3, output: "x\nmaintenance window still not restored: Travel Mode\n"),
                       "Toujours non rétabli : Travel Mode")
    }

    func testTakeBackCauseKeepsAnyOtherLineAndTheCodeFallback() {
        XCTAssertEqual(MaintenanceMonitor.takeBackFailureLine(status: 1, output: "the Mac is held by B, not by this session"),
                       "the Mac is held by B, not by this session")
        XCTAssertEqual(MaintenanceMonitor.takeBackFailureLine(status: 3, output: ""), "Reprendre a échoué (code 3)")
    }
}
