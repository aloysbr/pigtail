import CoreGraphics

/// Fixed identity of the Pigtail app: one bundle, a main window and a menu bar (D10, D11).
public enum AppIdentity {
    public static let bundleID = "sh.pigtail.app"
    public static let appName = "Pigtail"
    public static let windowAutosaveName = "PigtailMainWindow"
    /// pigtaild's default HTTP address (pkg/pigtail/client.go DefaultHTTPAddr).
    public static let defaultHTTPAddr = "127.0.0.1:7800"
    /// Content size at first launch; the window autosave wins afterwards (D10).
    public static let initialContentSize = CGSize(width: 1280, height: 800)
    /// Smallest content size the window accepts (D11).
    public static let minContentSize = CGSize(width: 640, height: 480)

    /// True only for the release bundle. A verify build (`sh.pigtail.app.verify`) or an
    /// executable outside a bundle (`nil`) asks for no notification and terminates no
    /// retired app.
    public static func isReleaseBundle(_ bundleID: String?) -> Bool {
        bundleID == Self.bundleID
    }
}
