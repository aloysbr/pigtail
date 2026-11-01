/// The page side of the window appearance bridge, injected by the app at
/// document end in the main frame only. It posts the user's CHOICE ("light",
/// "dark" or "system"), read from localStorage `pigtail-appearance`
/// (web/src/theme/appearance.ts; "system" when absent, unknown or blocked),
/// never the resolved `<html data-theme>`: Système resolves through the web
/// view's prefers-color-scheme, which follows the appearance this bridge
/// forces, so posting the resolved value would lock the app on it. The
/// attribute is only the trigger (the front saves the choice, then rewrites
/// it): once on load, then on each write of it. Nothing when the attribute is
/// absent (the native daemon-down page), nothing twice in a row, never an
/// exception. No change to the web front.
public enum AppearanceBridgeScript {
    public static let handlerName = "pigtailAppearance"

    public static let source = """
    (function () {
      var root = document.documentElement;
      var handlers = window.webkit && window.webkit.messageHandlers;
      if (!root || !handlers || !handlers.\(handlerName)) return;
      var last = null;
      function choice() {
        try {
          var c = window.localStorage.getItem('pigtail-appearance');
          return c === 'light' || c === 'dark' ? c : 'system';
        } catch (e) {
          return 'system';
        }
      }
      function post() {
        if (!root.dataset.theme) return;
        var c = choice();
        if (c === last) return;
        last = c;
        handlers.\(handlerName).postMessage(c);
      }
      post();
      new MutationObserver(post).observe(root, { attributes: true, attributeFilter: ['data-theme'] });
    })();
    """
}
