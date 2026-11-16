/*
 * The Mini App theme bootstrap.
 *
 * This file is loaded synchronously in <head>, BEFORE style.css and before app.js. It is deliberately a
 * separate self-hosted script rather than an inline one: the Mini App CSP is `script-src 'self'` with no
 * 'unsafe-inline', so an inline bootstrap would be blocked. Running before the stylesheet means the stored
 * theme is already on <html> when the first paint uses the palette, which removes the light-system flash of
 * the default sand palette for a user who chose graphite/ocean/forest.
 *
 * theme.js also mirrors the active --surface colour into every <meta name="theme-color">. The static media
 * fallback in index.html covers the no-JS / blocked-script case; that is the documented limitation.
 */
(() => {
  "use strict";

  const storageKey = "dsh-theme";
  const themeNames = ["graphite", "sand", "ocean", "forest", "lavender"];
  // --surface of each palette in style.css. A Go/JS smoke test keeps this map in sync with the stylesheet;
  // app.js reads the computed custom property on later switches and uses its own copy only as a fallback.
  const themeSurfaces = {
    graphite: "#1a1d25",
    sand: "#faf4ea",
    ocean: "#16212b",
    forest: "#182219",
    lavender: "#f8f5fb",
  };

  function storedTheme() {
    try {
      const value = localStorage.getItem(storageKey);
      return typeof value === "string" ? value : "";
    } catch (_) {
      return "";
    }
  }

  function initialTheme() {
    const stored = storedTheme();
    if (stored) return themeNames.includes(stored) ? stored : "graphite";
    try {
      if (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches) return "sand";
    } catch (_) { /* matchMedia is optional outside a browser */ }
    return "graphite";
  }

  function applyThemeColor(theme) {
    const color = themeSurfaces[theme] || themeSurfaces.graphite;
    for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
      meta.setAttribute("content", color);
    }
  }

  const theme = initialTheme();
  document.documentElement.setAttribute("data-theme", theme);
  applyThemeColor(theme);
})();
