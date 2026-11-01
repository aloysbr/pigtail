import Foundation

/// Browser launch helpers. Each function is fire-and-forget: errors are silently ignored.
public enum Browser {

    /// Open `http://localhost:<port>` in Arc.
    /// Runs `/usr/bin/open -a Arc <url>` in a detached Process.
    public static func openInArc(port: Int) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        process.arguments = ["-a", "Arc", "http://localhost:\(port)"]
        // Detach: do not track stdout/stderr
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try? process.run()
        // Do not call waitUntilExit — fire-and-forget
    }
}
