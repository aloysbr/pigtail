/* COPIE FIGÉE — source de vérité des valeurs de couleur du cockpit. NE PAS ÉDITER.
   Copie à l'identique du moteur de la maquette t4 (palette.js, validé en D5-D6).
   `npm run gen:palette` (scripts/gen-palette.mjs) en tire src/theme/palette.css ; palette-sync.test.ts garde les deux en phase. */
/* Cockpit DS et sessions — t4 (t3's, plus --veil for the image preview) · colour engine of the Hybride DS, now two shipped themes (D5).
   A theme is five numbers: ground hue gh, ground chroma gc (« chaleur »), accent hue ah, accent vividness ac
   (chroma cap) and depth ad (white-on-solid contrast aimed at). Each theme renders in light and dark.

   - Neutrals: oklch(L, gc × k, gh); L and k fixed per mode (t1 Hybride calibration), so every grey keeps its contrast
     whatever the theme. Ocean has gc = 0: pure neutral greys (a design setting), the hue then plays no part.
   - Accent « attend ta réponse »: hue ah, chroma ≤ ac inside sRGB; its lightness is solved per mode so that
     white on the solid ≥ 4.6:1, its text on its tint and on every ground ≥ 4.6:1, its dot ≥ 3:1 on the list ground.
   - Status colours (overdue / error red, CI green, today's amber) are shared by both themes and emitted here too (D6).

   Every token is emitted as a CSS oklch() string (inline custom properties on a .ds root, data-theme-palette names
   the theme); the same numbers feed the token table and its measures (WCAG contrast, ΔE OKLab). */
(function (root) {
  'use strict';

  /* ---------- OKLCH maths (Björn Ottosson's OKLab, sRGB D65) ---------- */
  function lchToLab(L, C, H) { var h = H * Math.PI / 180; return [L, C * Math.cos(h), C * Math.sin(h)]; }
  function labToLin(lab) {
    var L = lab[0], a = lab[1], b = lab[2];
    var l = L + 0.3963377774 * a + 0.2158037573 * b, m = L - 0.1055613458 * a - 0.0638541728 * b, s = L - 0.0894841775 * a - 1.2914855480 * b;
    l = l * l * l; m = m * m * m; s = s * s * s;
    return [4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s, -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s, -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s];
  }
  function linToLab(c) {
    var l = 0.4122214708 * c[0] + 0.5363325363 * c[1] + 0.0514459929 * c[2];
    var m = 0.2119034982 * c[0] + 0.6806995451 * c[1] + 0.1073969566 * c[2];
    var s = 0.0883024619 * c[0] + 0.2817188376 * c[1] + 0.6299787005 * c[2];
    l = Math.cbrt(l); m = Math.cbrt(m); s = Math.cbrt(s);
    return [0.2104542553 * l + 0.7936177850 * m - 0.0040720468 * s, 1.9779984951 * l - 2.4285922050 * m + 0.4505937099 * s, 0.0259040371 * l + 0.7827717662 * m - 0.8086757660 * s];
  }
  function inGamut(lin) { return lin.every(function (x) { return x >= -1e-4 && x <= 1 + 1e-4; }); }
  function clamp01(x) { return Math.min(1, Math.max(0, x)); }
  function gam(x) { x = clamp01(x); return x <= 0.0031308 ? 12.92 * x : 1.055 * Math.pow(x, 1 / 2.4) - 0.055; }
  function ungam(x) { return x <= 0.04045 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); }

  /* a colour = { L, C, H, a (alpha), lin (linear sRGB, clamped) } */
  function col(L, C, H, a) {
    var lin = labToLin(lchToLab(L, C, H)).map(clamp01);
    return { L: L, C: C, H: H, a: a == null ? 1 : a, lin: lin };
  }
  function fromHex(hex) {
    var h = hex.replace('#', ''), lin = [0, 2, 4].map(function (i) { return ungam(parseInt(h.slice(i, i + 2), 16) / 255); });
    var lab = linToLab(lin), C = Math.hypot(lab[1], lab[2]), H = (Math.atan2(lab[2], lab[1]) * 180 / Math.PI + 360) % 360;
    return { L: lab[0], C: C, H: H, a: 1, lin: lin };
  }
  /* largest chroma ≤ want that stays inside sRGB at (L, H) */
  function maxC(L, H, want) {
    if (inGamut(labToLin(lchToLab(L, want, H)))) return want;
    var lo = 0, hi = want;
    for (var i = 0; i < 24; i++) { var mid = (lo + hi) / 2; if (inGamut(labToLin(lchToLab(L, mid, H)))) lo = mid; else hi = mid; }
    return lo;
  }
  function Y(c) { return 0.2126 * c.lin[0] + 0.7152 * c.lin[1] + 0.0722 * c.lin[2]; }
  function contrast(fg, bg) { var a = Y(fg), b = Y(bg); return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); }
  /* ΔE in OKLab (Euclidean); 0.02 ≈ just noticeable */
  function dE(x, y) { var p = linToLab(x.lin), q = linToLab(y.lin); return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]); }
  function hex(c) { return '#' + c.lin.map(function (x) { var v = Math.round(gam(x) * 255); return (v < 16 ? '0' : '') + v.toString(16); }).join('').toUpperCase(); }
  function css(c) {
    var s = 'oklch(' + (+c.L.toFixed(4)) + ' ' + (+c.C.toFixed(4)) + ' ' + (+c.H.toFixed(1));
    return s + (c.a < 1 ? ' / ' + c.a : '') + ')';
  }

  /* ---------- neutral steps: [L, chroma factor k (× gc), alpha] — fixed per theme ---------- */
  var N = {
    light: {
      bg: [0.982, 0.55], side: [0.966, 0.95], surface: [1, 0], s3: [0.936, 1.1], hover: [0.948, 1.1], active: [0.927, 1.25], sel: [0.942, 1.1],
      line: [0.21, 0.3, 0.13], lineStrong: [0.21, 0.3, 0.28], kbdLine: [0.21, 0.3, 0.2], overlay: [0.19, 0.2, 0.22], veil: [0.19, 0.2, 0.56],
      text: [0.191, 0.2], text1: [0.359, 0.5], text2: [0.52, 0.95], text3: [0.724, 1.15], mid: [0.626, 1.2]
    },
    dark: {
      bg: [0.268, 0.5], side: [0.236, 0.4], surface: [0.308, 0.5], s3: [0.345, 0.7], hover: [0.3, 0.6], active: [0.336, 0.8], sel: [0.321, 0.8],
      line: [0.89, 1.4, 0.13], lineStrong: [0.89, 1.4, 0.28], kbdLine: [0.89, 1.4, 0.22], overlay: [0, 0, 0.5], veil: [0, 0, 0.72],
      text: [0.982, 0.55], text1: [0.807, 1.4], text2: [0.686, 1.2], text3: [0.523, 0.95], mid: [0.686, 1.2]
    }
  };
  /* ---------- shared status colours (both themes) — D6 ----------
     The overdue / error red moved toward crimson (hue 14) so the Terra accent, moved toward orange, stays apart from it;
     in dark it is also lighter, to reach 4.5:1 as text on the panel surface (t2's #E5534B gave 4.09:1 on the list).
     Green (CI), amber (today's star) unchanged from t2. */
  var STATUS = {
    light: { late: col(0.490, 0.195, 14), ok: fromHex('#4F6B33'), today: fromHex('#D4A11E'), white: fromHex('#FFFFFF') },
    dark: { late: col(0.702, 0.192, 14), ok: fromHex('#8DB06A'), today: fromHex('#E5B84A'), white: fromHex('#FFFFFF') }
  };
  /* t2's values, kept only to show the D6 before / after */
  var STATUS_T2 = {
    light: { late: fromHex('#A62B2B'), ok: fromHex('#4F6B33'), today: fromHex('#D4A11E'), white: fromHex('#FFFFFF') },
    dark: { late: fromHex('#E5534B'), ok: fromHex('#8DB06A'), today: fromHex('#E5B84A'), white: fromHex('#FFFFFF') }
  };
  var FIXED = STATUS;

  function neutrals(theme, gh, gc) {
    var t = N[theme], o = {};
    Object.keys(t).forEach(function (k) { var s = t[k]; o[k] = col(s[0], gc * s[1], gh, s[2]); });
    return o;
  }

  /* scan L from `from` toward `to`; first L where ok(colour) holds. C = min(want, gamut) at each L. */
  function solve(H, want, from, to, ok) {
    var step = from > to ? -0.002 : 0.002, c;
    for (var L = from; step < 0 ? L >= to : L <= to; L += step) {
      c = col(L, maxC(L, H, want), H);
      if (ok(c)) return c;
    }
    return c;
  }

  /* the accent family for one theme */
  function accent(theme, n, ah, ac, ad) {
    var dark = theme === 'dark', F = FIXED[theme], white = F.white;
    var o = {};
    /* solid fill under white text: the lightest fill that reaches the depth ad (white on fill, ≥ 4.6 so always ≥ 4.5) */
    var depth = Math.max(4.6, ad || 4.6);
    o.fill = solve(ah, ac, 0.8, 0.2, function (c) { return contrast(white, c) >= depth; });
    /* tint behind the « Arbitrage » / « Permission » chips and the cell of a waiting session */
    var tc = Math.min(Math.max(ac * (dark ? 0.2 : 0.14), dark ? 0.016 : 0.012), dark ? 0.036 : 0.03);
    o.soft = col(dark ? 0.31 : 0.952, maxC(dark ? 0.31 : 0.952, ah, tc), ah);
    /* text in the accent: ≥ 4.6 on its tint, on the ground and on a white / raised surface */
    var grounds = [o.soft, n.bg, n.surface, n.side];
    o.text = dark
      ? solve(ah, ac, 0.55, 0.95, function (c) { return grounds.every(function (g) { return contrast(c, g) >= 4.6; }); })
      : solve(ah, ac, 0.8, 0.2, function (c) { return grounds.every(function (g) { return contrast(c, g) >= 4.6; }); });
    /* the dot / the card's stripe: a graphic, ≥ 3:1 on the ground, as vivid as allowed */
    o.dot = dark
      ? solve(ah, ac * 1.05, 0.5, 0.95, function (c) { return contrast(c, n.bg) >= 3.4; })
      : solve(ah, ac * 1.05, 0.85, 0.2, function (c) { return contrast(c, n.bg) >= 3.0; });
    o.line = col(o.dot.L, o.dot.C, o.dot.H, dark ? 0.5 : 0.45);
    return o;
  }

  /* all colour tokens of the Hybride DS for one theme and mode; opts.status = STATUS_T2 measures t2's red instead */
  function tokens(mode, p, opts) {
    opts = opts || {};
    var F = (opts.status || STATUS)[mode];
    var n = neutrals(mode, p.gh, p.gc);
    /* hover on the sidebar ground: --hover reaches only 1.05:1 on the light side ground (DS floor 1.1:1), so it gets its own
       step: the first L from the side ground (darker in light, lighter in dark) whose contrast on it is ≥ 1.105, same hue
       and chroma family as --hover. Existing steps are untouched (D6). */
    n.hoverSide = solve(p.gh, p.gc * N[mode].hover[1], n.side.L, mode === 'dark' ? 0.95 : 0.05, function (c) { return contrast(c, n.side) >= 1.105; });
    /* D17: the current sidebar item, one step past the hover on the same ground: first L whose contrast on the side ground is
       ≥ 1.17 (hover sits at ≈ 1.11). Same hue and chroma family as --active. Existing steps untouched (D6). */
    n.activeSide = solve(p.gh, p.gc * N[mode].active[1], n.side.L, mode === 'dark' ? 0.95 : 0.05, function (c) { return contrast(c, n.side) >= (mode === 'dark' ? 1.3 : 1.17); });
    /* verify lot 2: the selected item of the ⌘K palette sits on --surface, where --active (dark) is only ≈ 1.1:1: its own step, first L from the
       surface whose contrast on it is ≥ 1.17 (floor 1.15 after rounding). Same hue and chroma family as --active. --active itself untouched. */
    n.activePal = solve(p.gh, p.gc * N[mode].active[1], n.surface.L, mode === 'dark' ? 0.95 : 0.05, function (c) { return contrast(c, n.surface) >= 1.17; });
    /* hover of a row sitting on --surface (the session dock's picker): in dark --hover (L 0.30) is a shade BELOW the surface
       (0.308), almost invisible; there it takes the --active-pal step (>= 1.17 on the surface). Light keeps --hover. */
    n.hoverPanel = mode === 'dark' ? n.activePal : n.hover;
    var a = accent(mode, n, p.ah, p.ac, p.ad), dark = mode === 'dark';
    var inkOnInk = dark ? col(0.191, p.gc * 0.2, p.gh) : n.bg;
    var v = {
      '--bg': n.bg, '--bg-side': n.side, '--surface': n.surface, '--surface-2': dark ? n.surface : n.side, '--surface-3': n.s3, '--panel-bg': n.surface,
      '--hover': n.hover, '--hover-side': n.hoverSide, '--hover-panel': n.hoverPanel, '--active-side': n.activeSide, '--active-pal': n.activePal, '--active': n.active, '--sel': n.sel,
      '--line': n.line, '--line-strong': n.lineStrong, '--head-line': n.line, '--group-line': n.line, '--kbd-line': n.kbdLine,
      '--text': n.text, '--text-1': n.text1, '--text-2': n.text2, '--text-3': n.text3,
      '--link': n.text, '--mark': n.text, '--focus': n.text, '--btn-fill': n.text, '--btn-on': inkOnInk,
      '--pill-on-line': n.text1, '--pill-on-bg': n.s3,
      '--accent': a.dot, '--accent-text': a.text,
      '--attn': a.dot, '--attn-fill': a.fill, '--attn-on': F.white, '--attn-text': a.text, '--attn-soft': a.soft, '--attn-line': a.line,
      '--wait-bg': n.surface,
      '--working': n.mid, '--gauge': n.mid, '--gauge-track': n.s3, '--gauge-high': n.text,
      '--s-cours': n.text1, '--s-ouvert': n.text2, '--s-standby': n.text3, '--s-done': n.text2,
      '--due-today-text': n.text1, '--warn': n.text1,
      '--pie': n.text2, '--chip-bg': dark ? n.s3 : n.hover, '--chip-text': n.text1, '--cell-bg': dark ? n.s3 : n.hover, '--cell-text': n.text1,
      '--kbd-bg': n.surface, '--overlay': n.overlay, '--code-bg': n.side,
      /* t4: the veil under the image preview (Quick Look), darker than the ⌘K scrim: the image is the only thing to read */
      '--veil': n.veil,
      '--due-late': F.late, '--bad': F.late, '--ok': F.ok, '--due-today': F.today
    };
    return { vars: v, n: n, a: a, fixed: F, mode: mode };
  }

  /* the measures shown in the token table and under the side-by-side views */
  function measures(t) {
    var n = t.n, a = t.a, F = t.fixed, grounds = [n.bg, n.side, n.surface];
    function onAll(c) { return Math.min.apply(null, grounds.map(function (g) { return contrast(c, g); })); }
    function acc(c) { return Math.min(dE(a.fill, c), dE(a.dot, c), dE(a.text, c)); }
    return {
      text: contrast(n.text, n.bg), text1: contrast(n.text1, n.bg), text2: Math.min(contrast(n.text2, n.bg), contrast(n.text2, n.side)), text3: contrast(n.text3, n.bg),
      fill: contrast(F.white, a.fill), atext: Math.min(contrast(a.text, a.soft), onAll(a.text)), atextTint: contrast(a.text, a.soft), dot: contrast(a.dot, n.bg),
      late: onAll(F.late), ok: onAll(F.ok), today: contrast(F.today, n.bg), grey: contrast(n.text3, n.bg),
      dLate: acc(F.late), dOk: acc(F.ok), dToday: acc(F.today)
    };
  }
  /* thresholds: text 4.5, graphics 3, ΔE OKLab 0.12 (under it two small marks read as one colour at a glance) */
  var MIN = { text: 4.5, graphic: 3, solid: 4.6, accentText: 4.6, dE: 0.12 };

  /* ---------- the two shipped themes (D5) ---------- */
  var THEMES = [
    { id: 'terra', name: 'Terra', gh: 95, gc: 0.010, ah: 53, ac: 0.135, ad: 5,
      mood: 'Ivoire chaud, accent terre cuite.', short: 'Ivoire et terre cuite' },
    { id: 'ocean', name: 'Ocean', gh: 0, gc: 0, ah: 275, ac: 0.19, ad: 4.6,
      mood: 'Gris pur, accent indigo (réglage de design).', short: 'Gris pur et indigo' }
  ];
  /* Terra as t2 shipped it (« Terracotta »), for the D6 before / after only */
  var TERRA_T2 = { id: 'terra-t2', name: 'Terra (t2)', gh: 95, gc: 0.010, ah: 40, ac: 0.135, ad: 5 };

  var api = { THEMES: THEMES, TERRA_T2: TERRA_T2, STATUS: STATUS, STATUS_T2: STATUS_T2, MIN: MIN, tokens: tokens, measures: measures, contrast: contrast, dE: dE, hex: hex, css: css, col: col, fromHex: fromHex };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.CKPal = api;
})(this);
