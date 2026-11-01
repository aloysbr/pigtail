import Foundation

/// pigtaild's socket path, resolved like the CLI: a non-empty
/// $PIGTAIL_SOCKET, else ~/.config/pigtail/pigtail.sock
/// (cmd/pigtail/main.go socketPath, pkg/pigtail/client.go NewSocketClient).
public enum SocketPath {
    public static func resolve(env: [String: String], home: String) -> String {
        if let s = env["PIGTAIL_SOCKET"], !s.isEmpty { return s }
        return (home as NSString).appendingPathComponent(".config/pigtail/pigtail.sock")
    }

    /// The path for this process.
    public static func current() -> String {
        resolve(env: ProcessInfo.processInfo.environment, home: NSHomeDirectory())
    }
}
