import Combine
import Foundation

/// Reads `window.json` every second (it never writes) and runs « Reprendre » — the user's own
/// take-back command, set in the app's defaults (`takeBackCommandKey`); none set, the button runs
/// nothing and says so.
@MainActor
public final class MaintenanceMonitor: ObservableObject {
    /// The program « Reprendre » runs and its arguments.
    public struct TakeBackCommand: Equatable, Sendable {
        public let executable: URL
        public let arguments: [String]
        public init(executable: URL, arguments: [String]) { self.executable = executable; self.arguments = arguments }
    }

    public struct TakeBackResult: Sendable {
        public let status: Int32
        public let output: String
        public init(status: Int32, output: String) { self.status = status; self.output = output }
    }
    public typealias TakeBackRunner = @Sendable (_ executable: URL, _ arguments: [String], _ environment: [String: String]) async -> TakeBackResult

    @Published public private(set) var display: MaintenanceChipDisplay = .hidden
    @Published public private(set) var isTakingBack = false

    private let windowURL: URL
    private let takeBackCommand: TakeBackCommand?
    private let now: () -> Date
    private let runner: TakeBackRunner
    private var takeBackError: String?
    private var pollingTask: Task<Void, Never>?

    public static var defaultWindowURL: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/state/pigtail/maintenance-window.json")
    }

    /// The defaults key of the take-back command: an array of strings, the program's path first (a
    /// leading `~` is the home directory), then its arguments. Absent or empty = no command.
    nonisolated public static let takeBackCommandKey = "maintenanceTakeBackCommand"

    /// The line the chip shows when « Reprendre » is clicked with no command set.
    nonisolated public static let noTakeBackCommandLine = "Aucune commande « Reprendre » configurée"

    /// The take-back command read from `defaults`, nil when the key is absent, empty or not an array
    /// of non-empty strings.
    nonisolated public static func configuredTakeBackCommand(defaults: UserDefaults = .standard) -> TakeBackCommand? {
        guard let raw = defaults.array(forKey: takeBackCommandKey) as? [String],
              let first = raw.first, !first.isEmpty, !raw.contains(where: \.isEmpty) else { return nil }
        let path = first == "~" || first.hasPrefix("~/")
            ? FileManager.default.homeDirectoryForCurrentUser.path + first.dropFirst()
            : first
        return TakeBackCommand(executable: URL(fileURLWithPath: path), arguments: Array(raw.dropFirst()))
    }

    public init(windowURL: URL = MaintenanceMonitor.defaultWindowURL,
                takeBackCommand: TakeBackCommand? = MaintenanceMonitor.configuredTakeBackCommand(),
                now: @escaping () -> Date = Date.init,
                runner: @escaping TakeBackRunner = MaintenanceMonitor.runProcess) {
        self.windowURL = windowURL
        self.takeBackCommand = takeBackCommand
        self.now = now
        self.runner = runner
    }

    /// One read + fold. A missing file is « no window » (hidden); a file that cannot be read or
    /// decoded is a failure shown, never a silent hide.
    public func poll() {
        var window: MaintenanceWindow?
        var readError = false
        do {
            window = try MaintenanceWindow.decode(Data(contentsOf: windowURL))
        } catch let e as CocoaError where e.code == .fileReadNoSuchFile || e.code == .fileNoSuchFile {
            window = nil
        } catch {
            readError = true
        }
        let next = MaintenanceChipDisplay.fold(window: window, readError: readError, takeBackError: takeBackError, now: now())
        if next != display { display = next }
    }

    public func startPolling(interval: TimeInterval = 1) {
        guard pollingTask == nil else { return }
        pollingTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                self.poll()
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }

    public func stopPolling() {
        pollingTask?.cancel()
        pollingTask = nil
    }

    /// Runs the take-back command, without the session id of whoever launched the bar, without a short
    /// timeout (restoring the machine can take ~100 s). A second click while one runs does nothing.
    /// With no command set it runs nothing and shows `noTakeBackCommandLine`.
    public func takeBack() async {
        guard !isTakingBack else { return }
        guard let command = takeBackCommand else {
            takeBackError = Self.noTakeBackCommandLine
            poll()
            return
        }
        isTakingBack = true
        takeBackError = nil
        poll()
        let result = await runner(command.executable, command.arguments,
                                  Self.takeBackEnvironment(from: ProcessInfo.processInfo.environment))
        if result.status != 0 { takeBackError = Self.takeBackFailureLine(status: result.status, output: result.output) }
        isTakingBack = false
        poll()
    }

    /// The one line the chip shows for a failed « Reprendre ».
    nonisolated public static func takeBackFailureLine(status: Int32, output: String) -> String {
        let last = output.split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }.last { !$0.isEmpty }
        guard let last else { return "Reprendre a échoué (code \(status))" }
        // Two English forms a take-back command may print last when the machine is not back to its
        // state before the window; they are shown in French. Anything else stays as is.
        for (english, french) in [("maintenance window closed — not restored: ", "Fenêtre fermée, non rétabli : "),
                                  ("maintenance window still not restored: ", "Toujours non rétabli : ")]
        where last.hasPrefix(english) {
            return french + last.dropFirst(english.count)
        }
        return last
    }

    nonisolated public static func takeBackEnvironment(from env: [String: String]) -> [String: String] {
        var e = env
        e.removeValue(forKey: "CLAUDE_CODE_SESSION_ID")
        return e
    }

    /// Default runner: a child process, stdout+stderr merged, awaited without blocking the main actor.
    nonisolated public static let runProcess: TakeBackRunner = { exe, args, env in
        await withCheckedContinuation { (cont: CheckedContinuation<TakeBackResult, Never>) in
            let p = Process()
            p.executableURL = exe
            p.arguments = args
            p.environment = env
            let pipe = Pipe()
            p.standardOutput = pipe
            p.standardError = pipe
            p.standardInput = FileHandle.nullDevice
            do {
                try p.run()
            } catch {
                cont.resume(returning: TakeBackResult(status: 127, output: "commande « Reprendre » introuvable"))
                return
            }
            DispatchQueue.global().async {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                cont.resume(returning: TakeBackResult(status: p.terminationStatus, output: String(decoding: data, as: UTF8.self)))
            }
        }
    }
}
