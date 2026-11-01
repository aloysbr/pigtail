import Foundation

// MARK: - PigtailLauncher

public enum PigtailLauncher {

    /// Resolves the `pigtail` executable.
    ///
    /// Search order:
    /// 1. Each directory in `pathEnv` (colon-separated), first match wins.
    /// 2. A sibling of the running executable (`executableDir/pigtail`).
    ///
    /// - Parameters:
    ///   - pathEnv: The value of the `PATH` environment variable (may be nil).
    ///   - executableDir: The directory containing the running executable.
    ///   - fileExists: Injected predicate — `true` when the given path is an executable file.
    /// - Returns: Absolute path to `pigtail`, or `nil` if not found.
    public static func resolveBinary(
        pathEnv: String?,
        executableDir: String,
        fileExists: (String) -> Bool
    ) -> String? {
        // 1. Walk PATH
        if let pathEnv {
            for dir in pathEnv.split(separator: ":").map(String.init) {
                let candidate = dir + "/pigtail"
                if fileExists(candidate) {
                    return candidate
                }
            }
        }

        // 2. Sibling of running executable
        let sibling = executableDir + "/pigtail"
        if fileExists(sibling) {
            return sibling
        }

        return nil
    }

    // MARK: - start

    public struct LaunchError: Error, CustomStringConvertible {
        public let description: String
    }

    /// Launches `pigtail start <worktree>:<server>` in a detached `Process`.
    ///
    /// Resolves the binary using the live environment, then execs it.
    /// Throws `LaunchError` if the `pigtail` binary cannot be found.
    public static func start(worktree: String, server: String) throws {
        let execDir: String
        if let url = Bundle.main.executableURL?.deletingLastPathComponent() {
            execDir = url.path
        } else {
            // Fallback: derive from argv[0]
            execDir = URL(fileURLWithPath: CommandLine.arguments[0])
                .deletingLastPathComponent().path
        }

        guard let binary = resolveBinary(
            pathEnv: ProcessInfo.processInfo.environment["PATH"],
            executableDir: execDir,
            fileExists: { FileManager.default.isExecutableFile(atPath: $0) }
        ) else {
            throw LaunchError(description: "pigtail binary not found in PATH or next to the app executable")
        }

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: binary)
        proc.arguments = ["start", "\(worktree):\(server)"]
        try proc.run()
        // Fire-and-forget: do not waitUntilExit
    }
}
