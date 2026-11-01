/// How Pigtail was launched, which decides whether the window opens.
/// A user launch (Finder, Spotlight, Dock, `open`) shows the window and the menu bar;
/// a login-item launch or `--background` (the relaunch after an install) shows the
/// menu bar alone. The raw value is the label of the `launch context:` log line.
public enum LaunchContext: String, Equatable, Sendable {
    case user
    case loginItem = "login-item"
    case background

    /// The argument `scripts/app-install.sh` passes when it relaunches Pigtail.
    public static let backgroundArgument = "--background"

    /// `launchedAsLoginItem` comes from the `oapp` Apple event (keyAELaunchedAsLogInItem),
    /// read in applicationDidFinishLaunching; it wins over the arguments.
    public static func decide(launchedAsLoginItem: Bool, arguments: [String]) -> LaunchContext {
        if launchedAsLoginItem { return .loginItem }
        if arguments.dropFirst().contains(backgroundArgument) { return .background }
        return .user
    }

    /// Only a user launch opens the window; at login the Mac shows the menu bar alone.
    public var opensWindow: Bool { self == .user }
}
