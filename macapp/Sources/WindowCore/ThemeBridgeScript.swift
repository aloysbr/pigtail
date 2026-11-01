/// The page side of the Dock icon bridge, injected by the app at document
/// end in the main frame only. It posts the palette `<html data-theme-palette>`
/// carries (web/src/theme/boot.js:24, appearance.ts applyChoice), then each
/// change of it, to `window.webkit.messageHandlers.pigtailTheme`. Nothing when
/// the attribute is absent (the native daemon-down page), nothing twice in a
/// row, never an exception. No change to the web front.
public enum ThemeBridgeScript {
    public static let handlerName = "pigtailTheme"

    public static let source = """
    (function () {
      var root = document.documentElement;
      var handlers = window.webkit && window.webkit.messageHandlers;
      if (!root || !handlers || !handlers.\(handlerName)) return;
      var last = null;
      function post() {
        var palette = root.dataset.themePalette;
        if (!palette || palette === last) return;
        last = palette;
        handlers.\(handlerName).postMessage(palette);
      }
      post();
      new MutationObserver(post).observe(root, { attributes: true, attributeFilter: ['data-theme-palette'] });
    })();
    """
}
