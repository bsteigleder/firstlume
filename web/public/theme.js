// Applies the saved theme before the first paint so the page never flashes the
// wrong one. Kept in sync with src/theme.ts; external because the CSP forbids inline scripts.
try {
  var t = localStorage.getItem("firstlume.theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch (e) {}
