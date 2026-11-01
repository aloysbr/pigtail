import AppKit

// One app: the menu bar and the main window, composed by AppDelegate.
// Accessory until a window shows (ActivationPolicyDecider).
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
