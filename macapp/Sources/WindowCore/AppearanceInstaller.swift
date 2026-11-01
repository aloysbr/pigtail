import Foundation
import WebKit

/// The WebKit side of the window appearance bridge: puts the appearance user
/// script and its message handler on a web view's configuration, and turns
/// each message into an appearance to apply. The AppKit side
/// (`NSApp.appearance`) stays in the app: it comes in as `onAppearance`, which
/// keeps this provable headless.
@MainActor
public final class AppearanceInstaller: NSObject, WKScriptMessageHandler {
    private let appearance: WindowAppearance
    private let onAppearance: @MainActor (AppearanceChoice) -> Void

    public init(appearance: WindowAppearance, onAppearance: @escaping @MainActor (AppearanceChoice) -> Void) {
        self.appearance = appearance
        self.onAppearance = onAppearance
    }

    /// Before the web view is created. Weak proxy as for the Dock icon bridge.
    public func install(on controller: WKUserContentController) {
        controller.addUserScript(WKUserScript(
            source: AppearanceBridgeScript.source,
            injectionTime: .atDocumentEnd,
            forMainFrameOnly: true
        ))
        controller.add(WeakScriptMessageHandler(self), name: AppearanceBridgeScript.handlerName)
    }

    public func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let next = appearance.receive(message.body) else { return }
        onAppearance(next)
    }
}
