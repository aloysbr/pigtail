import AppKit
import Combine
import SwiftUI

/// The chip's window: bottom centre of the screen, above the Dock, never key, never main, never
/// activating the app. It follows `MaintenanceMonitor.display`; `.hidden` takes it off screen.
@MainActor
public final class MaintenanceChipPanel: NSPanel {
    public private(set) var shownDisplay: MaintenanceChipDisplay = .hidden

    private let monitor: MaintenanceMonitor
    private let visibleFrame: () -> NSRect
    private let present: (NSPanel) -> Void
    private let dismiss: (NSPanel) -> Void
    private let hosting: ClickThroughHostingView
    private var cancellable: AnyCancellable?

    /// The chip's width on this screen: the stable 760 pt, never wider than the usable area (16 pt gutters).
    public static func chipWidth(in visible: NSRect) -> CGFloat {
        min(MaintenanceChipView.defaultWidth, max(240, visible.width - 32))
    }

    /// Bottom centre, 16 pt above the usable area's bottom edge (`visibleFrame` already excludes the Dock).
    public static func frame(size: NSSize, in visible: NSRect) -> NSRect {
        NSRect(x: visible.midX - size.width / 2, y: visible.minY + 16, width: size.width, height: size.height)
    }

    public init(monitor: MaintenanceMonitor,
                visibleFrame: @escaping () -> NSRect = { NSScreen.screens.first?.visibleFrame ?? .zero },
                present: @escaping (NSPanel) -> Void = { $0.orderFrontRegardless() },
                dismiss: @escaping (NSPanel) -> Void = { $0.orderOut(nil) }) {
        self.monitor = monitor
        self.visibleFrame = visibleFrame
        self.present = present
        self.dismiss = dismiss
        self.hosting = ClickThroughHostingView(rootView: AnyView(EmptyView()))
        super.init(contentRect: NSRect(x: 0, y: 0, width: 640, height: 80),
                   styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: true)
        level = .statusBar
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary]
        hidesOnDeactivate = false
        isReleasedWhenClosed = false
        isOpaque = false
        backgroundColor = .clear
        hasShadow = true
        // D4-B's backdrop-filter: a dark HUD material under the view's rgba(30,30,32,.82) fill, clipped to its corners.
        let fx = NSVisualEffectView(frame: NSRect(x: 0, y: 0, width: 640, height: 80))
        fx.material = .hudWindow
        fx.blendingMode = .behindWindow
        fx.state = .active
        fx.appearance = NSAppearance(named: .darkAqua)
        fx.wantsLayer = true
        fx.layer?.cornerRadius = 22
        fx.layer?.cornerCurve = .continuous
        fx.layer?.masksToBounds = true
        hosting.frame = fx.bounds
        hosting.autoresizingMask = [.width, .height]
        fx.addSubview(hosting)
        contentView = fx
        cancellable = monitor.$display.combineLatest(monitor.$isTakingBack)
            .sink { [weak self] display, taking in self?.apply(display: display, taking: taking) }
        NotificationCenter.default.addObserver(forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.relayout() }
        }
    }

    public override var canBecomeKey: Bool { false }
    public override var canBecomeMain: Bool { false }

    /// « Reprendre ». The returned task is for tests; the click handler drops it.
    @discardableResult
    public func takeBackTapped() -> Task<Void, Never> {
        Task { [monitor] in await monitor.takeBack() }
    }

    private func apply(display: MaintenanceChipDisplay, taking: Bool) {
        shownDisplay = display
        if case .hidden = display { dismiss(self); return }
        hosting.rootView = AnyView(MaintenanceChipView(display: display, isTakingBack: taking, width: Self.chipWidth(in: visibleFrame())) { [weak self] in self?.takeBackTapped() })
        relayout()
        present(self)
    }

    private func relayout() {
        guard case .hidden = shownDisplay else {
            hosting.layoutSubtreeIfNeeded()
            let size = hosting.fittingSize
            setFrame(Self.frame(size: size, in: visibleFrame()), display: false)
            invalidateShadow()
            return
        }
    }
}

/// The first click on the chip is the click on « Reprendre »: the panel is never key.
final class ClickThroughHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
