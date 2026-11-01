/// Whether opening the menu bar popover activates the app. With the main window
/// visible, activating would bring that window to the front: the non-activating
/// panel becomes key on its own instead.
public enum PanelActivation {
    public static func shouldActivateApp(mainWindowVisible: Bool) -> Bool {
        !mainWindowVisible
    }
}
