/// The app's activation policy: `.regular` shows the Dock icon and the menus,
/// `.accessory` keeps only the menu bar item.
public enum AppPolicy: Equatable, Sendable {
    case regular, accessory
}

/// D1: closing the window does not quit; the Dock icon shows only while the main
/// window is open. A miniaturized window counts as open, or it could not be found
/// again (it lives in the Dock).
public enum ActivationPolicyDecider {
    public static func policy(mainWindowOpen: Bool, mainWindowMiniaturized: Bool) -> AppPolicy {
        mainWindowOpen || mainWindowMiniaturized ? .regular : .accessory
    }
}
