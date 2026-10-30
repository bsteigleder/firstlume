import { useState } from "react";

// "system" follows the OS; the others pin a theme via <html data-theme>.
export type Theme = "system" | "light" | "dark";

const key = "firstlume.theme";

function load(): Theme {
  try {
    const t = localStorage.getItem(key);
    return t === "light" || t === "dark" ? t : "system";
  } catch {
    return "system";
  }
}

function apply(t: Theme) {
  if (t === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
  try {
    if (t === "system") localStorage.removeItem(key);
    else localStorage.setItem(key, t);
  } catch {
    // storage unavailable: the choice lasts until reload
  }
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, setTheme] = useState(load);
  return [
    theme,
    (t) => {
      apply(t);
      setTheme(t);
    },
  ];
}
