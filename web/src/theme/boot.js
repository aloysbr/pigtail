/* Pigtail boot: sets the theme before the first paint (no flash of another theme nor of light).
   Inlined at the head of <head> by src/theme/boot-plugin.ts, before any stylesheet. Plain ES5, depends on nothing,
   never throws. Must agree with readChoice + applyChoice of src/theme/appearance.ts (appearance.test.ts). */
(function () {
  var root = document.documentElement;
  var palette = 'terra';
  var appearance = 'system';
  try {
    /* One-time migration of the legacy cockpit-* keys: copy when the new key is missing, then drop the old one. */
    try {
      var ls = window.localStorage;
      var K = ['palette', 'appearance', 'scopes', 'side', 'kb', 'side-w', 'panel-w'];
      for (var i = 0; i < K.length; i++) {
        var o = 'cockpit-' + K[i], n = 'pigtail-' + K[i], v = ls.getItem(o);
        if (v !== null) {
          if (ls.getItem(n) === null) ls.setItem(n, v);
          ls.removeItem(o);
        }
      }
    } catch (e) {
      /* storage blocked: nothing to migrate */
    }
    var p = window.localStorage.getItem('pigtail-palette');
    var a = window.localStorage.getItem('pigtail-appearance');
    if (p === 'terra' || p === 'ocean') palette = p;
    if (a === 'light' || a === 'dark' || a === 'system') appearance = a;
  } catch (e) {
    /* private window or blocked site data: the defaults */
  }
  var dark = appearance === 'dark';
  if (appearance === 'system') {
    try {
      dark = !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
    } catch (e) {
      dark = false;
    }
  }
  root.setAttribute('data-theme-palette', palette);
  root.setAttribute('data-theme', dark ? 'dark' : 'light');
})();
