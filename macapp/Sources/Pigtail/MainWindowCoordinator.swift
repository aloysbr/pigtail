import AppCore
import AppKit
import WindowCore
import WebKit

/// The main window's life: one `NSWindow`, created at the first `show()` and
/// kept (its frame and autosave survive a close); one web view and its flow
/// per opening, released when the window closes. Closing the window never
/// quits Pigtail: the menu bar stays (`AppLifecycle`).
@MainActor
final class MainWindowCoordinator: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var controller: MainWindowController?
    private var runner: FlowRunner?
    private let dockIcon = DockIconBridge()
    private let appearance = AppearanceBridge()
    private var keyRequest = WindowKeyRequest()

    /// Read by the popover: an open panel activates the app only when the window is not on screen.
    var isWindowVisible: Bool { window?.isVisible ?? false }

    override init() {
        super.init()
        dockIcon.applySaved()
    }

    func show() {
        let window = self.window ?? makeWindow()
        if controller == nil {
            let controller = MainWindowController(window: window, dockIcon: dockIcon, appearance: appearance,
                                                  seed: Self.seedScript())
            self.controller = controller
            runner = FlowRunner(controller: controller)
        }
        if window.isMiniaturized { window.deminiaturize(nil) }
        applyPolicy(windowOpen: true)
        NSApp.activate()
        apply(keyRequest.handle(.show))
        // The custom Dock icon is lost at each activation policy change.
        dockIcon.applySaved()
        runner?.start()
        DispatchQueue.main.async {
            AppDelegate.log.notice("[Pigtail] window show requested: key=\(window.isKeyWindow, privacy: .public) active=\(NSApp.isActive, privacy: .public)")
        }
    }

    private func apply(_ action: WindowKeyRequest.Action) {
        if action == .makeKey { window?.makeKeyAndOrderFront(nil) }
    }

    /// The app became active: a key request still due is replayed.
    func appDidBecomeActive() {
        apply(keyRequest.handle(.appBecameActive))
    }

    /// View › Recharger (⌘R): goes through the flow, never webView.reload(); nothing without a window.
    @objc func reload(_ sender: Any?) {
        runner?.reload()
    }

    private func makeWindow() -> NSWindow {
        let window = NSWindow(
            contentRect: NSRect(origin: .zero, size: AppIdentity.initialContentSize),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = AppIdentity.appName
        window.isReleasedWhenClosed = false
        window.contentMinSize = AppIdentity.minContentSize // D11
        // D10: 1280×800 at first launch only; the saved frame wins afterwards.
        if !window.setFrameUsingName(AppIdentity.windowAutosaveName) {
            window.center()
        }
        // Once, at creation: a second call with the same name on a live window is refused.
        if !window.setFrameAutosaveName(AppIdentity.windowAutosaveName) {
            AppDelegate.log.error("[Pigtail] window frame autosave refused")
        }
        window.delegate = self
        appearance.window = window
        self.window = window
        return window
    }

    /// The saved native choices, so a fresh web storage does not start on the web's defaults.
    /// « Saved » = the key is present, never the stores' default value.
    private static func seedScript() -> String? {
        // The key of DockIconStore (WindowCore), whose `load()` reads an absent palette as Océan.
        let palette = UserDefaults.standard.string(forKey: "dockPalette").flatMap(ThemePalette.init(rawValue:))
        return PreferenceSeedScript.source(palette: palette, appearance: WindowAppearanceStore().load())
    }

    // MARK: Activation policy

    private func applyPolicy(windowOpen: Bool? = nil) {
        let open = windowOpen ?? (window?.isVisible ?? false)
        let policy = ActivationPolicyDecider.policy(mainWindowOpen: open, mainWindowMiniaturized: window?.isMiniaturized ?? false)
        NSApp.setActivationPolicy(policy == .regular ? .regular : .accessory)
        AppDelegate.log.notice("[Pigtail] policy: \(policy == .regular ? "regular" : "accessory", privacy: .public)")
    }

    // MARK: NSWindowDelegate

    func windowWillClose(_ notification: Notification) {
        _ = keyRequest.handle(.closed)
        runner?.handle(.windowClosed)
        runner = nil
        controller?.tearDown()
        controller = nil
        AppDelegate.log.notice("[Pigtail] web view released")
        applyPolicy(windowOpen: false)
    }

    func windowDidMiniaturize(_ notification: Notification) { applyPolicy() }
    func windowDidDeminiaturize(_ notification: Notification) {
        applyPolicy()
        apply(keyRequest.handle(.deminiaturized))
    }

    func windowDidBecomeKey(_ notification: Notification) {
        _ = keyRequest.handle(.becameKey)
        AppDelegate.log.notice("[Pigtail] window became key")
    }
}
