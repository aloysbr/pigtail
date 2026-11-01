import Foundation
import WebKit

/// The WebKit side of the Dock icon bridge: puts the theme user script and
/// its message handler on a web view's configuration, and turns each message
/// into a palette to apply. The AppKit side (`applicationIconImage`) stays in
/// the app: it comes in as `onPalette`, which keeps this provable headless.
@MainActor
public final class DockIconInstaller: NSObject, WKScriptMessageHandler {
    private let theme: DockIconTheme
    private let onPalette: @MainActor (ThemePalette) -> Void

    public init(theme: DockIconTheme, onPalette: @escaping @MainActor (ThemePalette) -> Void) {
        self.theme = theme
        self.onPalette = onPalette
    }

    /// Before the web view is created. The handler is registered through a
    /// weak proxy: guard in case the bridge ever retains the view (today it
    /// holds neither the view nor the controller, so there is no cycle).
    public func install(on controller: WKUserContentController) {
        controller.addUserScript(WKUserScript(
            source: ThemeBridgeScript.source,
            injectionTime: .atDocumentEnd,
            forMainFrameOnly: true
        ))
        controller.add(WeakScriptMessageHandler(self), name: ThemeBridgeScript.handlerName)
    }

    public func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let palette = theme.receive(message.body) else { return }
        onPalette(palette)
    }
}

/// Forwards to a handler it does not retain.
@MainActor
final class WeakScriptMessageHandler: NSObject, WKScriptMessageHandler {
    private weak var target: WKScriptMessageHandler?

    init(_ target: WKScriptMessageHandler) {
        self.target = target
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(userContentController, didReceive: message)
    }
}
