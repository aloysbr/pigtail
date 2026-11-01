import AppKit
import Combine
import SwiftUI
import AppCore
import MenuBarCore

@MainActor
final class MenuBarController: NSObject, NSWindowDelegate {
    private var statusItem: NSStatusItem!
    private var panel: NSPanel?
    private var globalEventMonitor: Any?
    private var localEventMonitor: Any?

    /// Screen-space Y of the panel's top edge while open. An NSWindow grows from
    /// its bottom-left origin (upward), which makes content expansion look like the
    /// popover re-centers/opens upward. We pin this top edge on every resize so the
    /// panel grows downward instead. Nil while the panel is closed.
    private var anchoredTopY: CGFloat?
    /// Decides the window's size from what the card measures (open: final size at once; close: shrink at the end).
    private lazy var sizeDriver = PanelSizeDriver(
        apply: { [weak self] size in self?.resizePanel(to: size) },
        windowHeight: { [weak self] in self?.panel?.frame.height ?? 0 }
    )

    private let client: any DaemonClienting = DaemonClient()
    private var store: PopoverStore!
    private let loginItem: LoginItemController
    /// The main window: « Ouvrir Pigtail », and whether opening the panel activates the app.
    private let coordinator: MainWindowCoordinator

    init(coordinator: MainWindowCoordinator, loginItem: LoginItemController) {
        self.coordinator = coordinator
        self.loginItem = loginItem
        super.init()
    }

    // Travel Mode (GET /lid every 60 s, D18, + /events lid.* (D26)): for the
    // app's lifetime, popover open or not — never stopped in `closePanel()`.
    // It carries its own optimism for the section's actions, so the store
    // never sees /lid.
    // Each new automatic cut it reads becomes a macOS notification (D3),
    // posted by `lidNotifier`. A `lazy var`: a stored property's initialiser
    // cannot reach `self.lidNotifier`; `[weak self]` avoids the cycle.
    private lazy var lidMonitor: LidMonitor = LidMonitor(
        client: DaemonClient(),
        onNotice: { [weak self] notice in self?.lidNotifier.post(notice) })
    // Posts the Travel Mode notices and says whether they can show (Q2).
    private let lidNotifier = LidNotifier()
    // Maintenance window chip (window.json, 1 s): read-only, for the app's lifetime. The panel is
    // non-activating and never key, so it cannot take the Mac's focus.
    private let maintenanceMonitor = MaintenanceMonitor()
    private lazy var maintenanceChip = MaintenanceChipPanel(monitor: maintenanceMonitor)
    private lazy var lidEventWatcher = LidEventWatcher(source: DaemonClient(), target: lidMonitor)
    // The cmux check notice (D2, sub_x000000000000037): one notification per
    // cmux launch when something is left for the user, posted by `lidNotifier`.
    private lazy var checkNoticeMonitor = CheckNoticeMonitor(
        client: DaemonClient(), ledger: DefaultsNoticeLedger(),
        onNotice: { [weak self] n in self?.lidNotifier.post(id: n.id, title: n.title, body: n.body) })
    private lazy var checkNoticeEventWatcher = CheckNoticeEventWatcher(source: DaemonClient(), target: checkNoticeMonitor)
    // The low-disk notice (journal disk-low → GET /alerts + /events disk.*): one
    // notification per crossing, withdrawn on disk.ok, shown again at start when the
    // alert is already open. Posted by `lidNotifier`, which is a no-op outside the
    // release bundle.
    private lazy var diskMonitor = DiskMonitor(
        reader: DaemonClient(),
        onAction: { [weak self] action in
            switch action {
            case .post(let id, let title, let body): self?.lidNotifier.post(id: id, title: title, body: body)
            case .remove(let id): self?.lidNotifier.remove(id: id)
            }
        })
    private lazy var diskEventWatcher = DiskEventWatcher(source: DaemonClient(), target: diskMonitor)
    // GET /usage/overview (60 s): polled only while the popover is open (started in showPanel, stopped in closePanel).
    private let usageMonitor = UsageMonitor(client: DaemonClient())
    private var lidCancellable: AnyCancellable?

    // MARK: - Status item rendering

    /// Draws the status item — image, Travel Mode title, accessibility label, tooltip — for one
    /// Travel Mode display. The only place any of those is set after launch; called from the
    /// single subscription in `start()` and from the title's minute timer.
    /// No badge (D9): the glyph is always the bare head, the title next to it (R9) is the signal.
    private func renderStatusItem(lid: LidDisplay) {
        guard let button = statusItem.button else { return }
        lastRender = lid
        // Travel Mode (D23): "Travel Mode 3 h" to the right of the head while it is on, the head
        // alone otherwise.
        let now = Date()
        let title = LidStatusTitle.title(for: lid, now: now)
        statusItem.length = StatusItemImage.statusItemLength(title: title)
        button.imagePosition = title == nil ? .imageOnly : .imageLeft
        button.title = title ?? ""
        scheduleTitleRefresh(lid: lid, now: now)
        button.image = StatusItemImage.base()
        button.setAccessibilityLabel(StatusItemSignal.accessibilityLabel(lid: lid))
        button.toolTip = StatusItemSignal.tooltip(lid: lid)
    }

    /// The display of the last render, so the title's minute timer can render again without a request.
    private var lastRender: LidDisplay?
    /// One-shot timer at the next instant the Travel Mode text changes
    /// (`LidStatusTitle.nextRefresh`, at most 60 s away), re-armed by every render.
    private var titleTimer: Timer?

    private func scheduleTitleRefresh(lid: LidDisplay, now: Date) {
        titleTimer?.invalidate()
        titleTimer = nil
        guard let next = LidStatusTitle.nextRefresh(for: lid, now: now) else { return }
        // A hair past the boundary, so the rounding reads the new minute.
        let timer = Timer(fire: next.addingTimeInterval(0.05), interval: 0, repeats: false) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, let lid = self.lastRender else { return }
                self.renderStatusItem(lid: lid)
            }
        }
        // .common: the text keeps ticking while a menu or the popover tracks events.
        RunLoop.main.add(timer, forMode: .common)
        titleTimer = timer
    }

    // MARK: - Start

    /// Called once by AppDelegate, after the legacy preferences are carried (the
    /// status item's position depends on them). The login item is bootstrapped there too.
    func start() {
        store = PopoverStore(client: client)

        // Prime the store once at launch, the lighter cousin of
        // `lidMonitor.startPolling()` below: that one polls forever
        // because the status item must stay right popover-closed, this one
        // fires exactly once because the row list only matters once
        // someone opens the popover. Without it, `state` sits at its
        // constructor's daemonDown placeholder until `showPanel()` starts
        // polling — so the popover paints empty and fires three requests
        // on every single open. One refresh here means the very first
        // open already has (possibly slightly stale) content, and
        // `showPanel()`'s own refresh still runs unchanged right after.
        // Deliberately NOT `store.startPolling()`: that loop remains
        // exclusively started by `showPanel()` and stopped by
        // `closePanel()` — this is priming, not a second poll loop.
        // A daemon that is unreachable at launch is not a special case:
        // `refresh()`'s own catch already rebuilds `state` as a whole
        // daemonDown value (see PopoverStore.swift), never a partial one,
        // so a failed prime reads exactly like "nothing has loaded yet".
        Task { @MainActor in
            await store.refresh()
        }

        // Build actions — the mutating action is owned by the store, which applies an optimistic
        // state change immediately (so the click feels instant), then fires the daemon call and
        // reconciles via refresh.
        let actions = PopoverActions(
            stopAll: { [weak self] in
                self?.store.stopAll()
            },
            quit: {
                NSApp.terminate(nil)
            },
            openWindow: { [weak self] in
                self?.closePanel()
                self?.coordinator.show()
            }
        )

        // Status bar button
        statusItem = NSStatusBar.system.statusItem(withLength: StatusItemImage.statusItemLength(title: nil))
        if let button = statusItem.button {
            // No `button.title` at launch: AppKit lays a title and an image
            // out *side by side*, and that is exactly what D23 uses — the
            // title is set only while Travel Mode is on ("Travel Mode 3 h"
            // to the right of the head, `.imageLeft`), empty otherwise.
            button.image = StatusItemImage.base()
            button.imagePosition = .imageOnly
            button.setAccessibilityLabel(StatusItemSignal.accessibilityLabel(lid: .loading))
            button.target = self
            button.action = #selector(togglePanel(_:))
        }

        // Subscribe once, for the app's lifetime, so the status item stays current whether or not
        // the popover is open. The value is taken from the publisher's output, never re-read off
        // `lidMonitor`: `@Published` emits in `willSet`, before the property holds the new value.
        lidCancellable = lidMonitor.$display
            .receive(on: DispatchQueue.main)
            .sink { [weak self] lid in
                self?.renderStatusItem(lid: lid)
            }
        lidNotifier.start()         // its delegate is set before the first notice
        lidMonitor.startPolling()   // 60 s, D18
        _ = maintenanceChip         // subscribes before the first read
        maintenanceMonitor.startPolling()   // 1 s: the chip shows the maintenance window
        lidEventWatcher.start()     // lid.changed → re-read (D26)
        checkNoticeMonitor.startPolling()   // 60 s + cmux.check_notice events
        checkNoticeEventWatcher.start()
        diskEventWatcher.start()            // disk.low / disk.ok + GET /alerts at each connection

        // Build the borderless NSPanel once; content is drawn fully in SwiftUI.
        // The panel is transparent so the SwiftUI "card" carries the visuals.
        // SwiftUI owns the animation; the window is a passive mirror of the card's
        // current (animated) size, repositioned to pin its top edge. The view
        // reports its size via onSizeChange and we drive the frame in resizePanel.
        let hostView = PopoverHostView(
            store: store,
            lidMonitor: lidMonitor,
            lidNotifier: lidNotifier,
            usageMonitor: usageMonitor,
            loginItem: loginItem,
            actions: actions,
            sizeDriver: sizeDriver,
            onSizeChange: { [weak self] measure in
                MainActor.assumeIsolated { self?.sizeDriver.report(measure) }
            }
        )
        let hosting = NSHostingController(rootView: hostView)
        // The window follows the card through PanelSizeDriver, never the other way round.
        hosting.sizingOptions = []
        // Deliberately NO sizingOptions = [.preferredContentSize]: AppKit's
        // auto-resize anchors the bottom-left origin (grows upward), which fights
        // our top-pinning and causes resize-then-reposition jitter. We size the
        // window ourselves from the SwiftUI content size instead.

        let p = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 320, height: 100),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        p.isFloatingPanel = true
        p.level = .popUpMenu
        p.hidesOnDeactivate = true
        p.backgroundColor = .clear
        p.isOpaque = false
        // No native window shadow: the panel is transparent and resizes on every
        // open/refresh (setContentSize). AppKit caches a transparent window's
        // shadow on its old content shape and doesn't recompute it on resize,
        // leaving a ghost rounded rect offset behind the card. The SwiftUI
        // `.shadow()` on the card tracks the real shape, so it's the sole shadow.
        p.hasShadow = false
        p.isMovable = false
        p.worksWhenModal = false
        p.contentViewController = hosting

        p.delegate = self
        panel = p
    }

    // MARK: - Panel toggle

    @objc func togglePanel(_ sender: Any?) {
        guard let p = panel else { return }

        if p.isVisible {
            closePanel()
        } else {
            showPanel()
        }
    }

    // MARK: - NSWindowDelegate

    func windowDidResignKey(_ notification: Notification) {
        // Routes Cmd-Tab / click-another-app dismissal through the single close path.
        closePanel()
    }

    // MARK: - Content-driven sizing

    /// Mirrors the panel frame to the SwiftUI content size, pinning the top edge so
    /// content changes (disclosure expand/collapse, server list updates) grow the
    /// panel downward. SwiftUI drives the animation and calls this every frame via
    /// onSizeChange; a single atomic setFrame (size + origin together) avoids the
    /// resize-then-reposition jitter of the old windowDidResize correction.
    private func resizePanel(to size: CGSize) {
        guard let p = panel, p.isVisible, let top = anchoredTopY else { return }
        guard size.width > 1, size.height > 1 else { return }
        let newFrame = NSRect(
            x: p.frame.origin.x,
            y: top - size.height,
            width: size.width,
            height: size.height
        )
        if newFrame != p.frame {
            p.setFrame(newFrame, display: true)
            // Lay the card out for the new window now, so an animation started right after sees it.
            p.contentView?.layoutSubtreeIfNeeded()
        }
    }

    // MARK: - Panel helpers

    /// Removes any active event monitors and nils their refs.
    private func removeMonitors() {
        if let monitor = globalEventMonitor {
            NSEvent.removeMonitor(monitor)
            globalEventMonitor = nil
        }
        if let monitor = localEventMonitor {
            NSEvent.removeMonitor(monitor)
            localEventMonitor = nil
        }
    }

    private func showPanel() {
        // Guard against re-entrant / double show: drop stale monitors first.
        removeMonitors()

        guard let p = panel,
              let button = statusItem.button,
              let buttonWindow = button.window else { return }

        // Force the hosting controller to lay out so the panel adopts its
        // content-driven (preferredContentSize) size before positioning.
        p.layoutIfNeeded()
        // The hosting controller has no sizingOptions: ask it for the card's ideal size.
        var size = (p.contentViewController as? NSHostingController<PopoverHostView>)?
            .sizeThatFits(in: CGSize(width: PopoverContent.width + 2 * PopoverMetrics.shadowMargin, height: 10_000)) ?? .zero
        if size.width < 1 { size.width = 320 }
        if size.height < 1 { size.height = 200 } // fallback if SwiftUI hasn't sized yet
        p.setContentSize(size)

        // Convert the status-item button bounds to screen coordinates and
        // position the panel just below it (4 px gap), clamped to the screen.
        let buttonBoundsInWindow = button.convert(button.bounds, to: nil)
        let buttonScreenRect = buttonWindow.convertToScreen(buttonBoundsInWindow)
        var originX = buttonScreenRect.midX - size.width / 2
        // Anchor the card top (not the window top) 4 px below the menu bar: the
        // card is inset by shadowMargin inside the transparent window, so add it
        // back, otherwise the visible card hangs that far too low.
        let originY = buttonScreenRect.minY - size.height - 4 + PopoverMetrics.shadowMargin
        if let screen = NSScreen.screens.first(where: { $0.frame.contains(buttonScreenRect.origin) })
            ?? NSScreen.main {
            let visible = screen.visibleFrame
            originX = max(visible.minX + 4, min(originX, visible.maxX - size.width - 4))
        }

        p.setFrameOrigin(NSPoint(x: originX, y: originY))
        // Pin the current top edge so later content-driven resizes grow downward.
        anchoredTopY = p.frame.maxY
        // Activating the app would bring the main window forward with the panel: only without it on screen.
        if PanelActivation.shouldActivateApp(mainWindowVisible: coordinator.isWindowVisible) {
            NSApp.activate()
        }
        p.makeKeyAndOrderFront(nil)

        store.startPolling()
        Task { @MainActor in
            await store.refresh()
        }
        usageMonitor.startPolling(interval: 60)
        Task { @MainActor in
            await usageMonitor.poll()
        }
        Task { @MainActor in
            await lidMonitor.poll()
        }
        Task { @MainActor in
            await lidNotifier.refresh()
        }

        // Dismiss on any click outside the panel.
        globalEventMonitor = NSEvent.addGlobalMonitorForEvents(
            matching: [.leftMouseDown, .rightMouseDown]
        ) { [weak self] _ in
            Task { @MainActor in
                self?.closePanel()
            }
        }

        // Dismiss on Escape.
        localEventMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            if event.keyCode == 53 { // Escape
                Task { @MainActor in
                    self?.closePanel()
                }
                return nil
            }
            return event
        }
    }

    private func closePanel() {
        // Idempotency guard: if the panel is already hidden there is nothing to do.
        // This also breaks the re-entrancy loop:
        //   windowDidResignKey → closePanel → orderOut → resignKey → windowDidResignKey
        // Because by the time the second resignKey fires the panel is no longer visible.
        guard let p = panel, p.isVisible else { return }

        p.orderOut(nil)
        anchoredTopY = nil
        sizeDriver.reset()
        store.stopPolling()
        usageMonitor.stopPolling()
        // `lidMonitor` keeps polling for the app's whole lifetime: the status item reads it while
        // the popover is closed, so it is never stopped from here.
        removeMonitors()
    }
}

// MARK: - PopoverHostView

/// Thin SwiftUI wrapper that subscribes to PopoverStore as an ObservedObject.
private struct PopoverHostView: View {
    @ObservedObject var store: PopoverStore
    // Observed alongside `store`, not folded into it: the usage read has its own cadence (60 s) and
    // freezes its last answer on a failed read.
    @ObservedObject var lidMonitor: LidMonitor
    @ObservedObject var lidNotifier: LidNotifier
    @ObservedObject var usageMonitor: UsageMonitor
    @ObservedObject var loginItem: LoginItemController
    let actions: PopoverActions
    let sizeDriver: PanelSizeDriver
    let onSizeChange: (PanelMeasure) -> Void

    var body: some View {
        PopoverView(
            state: store.state,
            loaded: store.loaded,
            usage: usageMonitor.display,
            lidDisplay: lidMonitor.display,
            lidNotificationsDenied: lidNotifier.denied,
            detailExpanded: sizeDriver.preparing($usageMonitor.detailExpanded),
            loginItem: loginItem,
            actions: actions,
            onLidAction: { action in Task { await lidMonitor.perform(action) } },
            onSizeChange: onSizeChange
        )
        // The window can be taller than the card (a fold opening, a fold still closing): the card
        // hangs from the top edge and never moves; the transparent rest stays empty.
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    }
}
