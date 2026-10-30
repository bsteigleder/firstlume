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

const themeOptions: { value: Theme; label: string; icon: React.ReactNode }[] = [
  {
    value: "system",
    label: "Match system",
    icon: (
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden>
        <rect x="1.75" y="2.75" width="12.5" height="8.5" rx="1.5" />
        <path d="M5.5 14h5M8 11.25V14" strokeLinecap="round" />
      </svg>
    ),
  },
  {
    value: "light",
    label: "Light",
    icon: (
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden>
        <circle cx="8" cy="8" r="2.75" />
        <path d="M8 1.5v1.25M8 13.25v1.25M1.5 8h1.25M13.25 8h1.25M3.4 3.4l.9.9M11.7 11.7l.9.9M3.4 12.6l.9-.9M11.7 4.3l.9-.9" />
      </svg>
    ),
  },
  {
    value: "dark",
    label: "Dark",
    icon: (
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden>
        <path d="M13.5 9.6A5.75 5.75 0 0 1 6.4 2.5a5.75 5.75 0 1 0 7.1 7.1Z" />
      </svg>
    ),
  },
];

export function ThemeSwitch() {
  const [theme, setTheme] = useTheme();
  return (
    <div className="theme-switch" role="group" aria-label="Theme">
      {themeOptions.map((o) => (
        <button key={o.value} type="button" aria-pressed={theme === o.value} title={o.label} aria-label={o.label} onClick={() => setTheme(o.value)}>
          {o.icon}
        </button>
      ))}
    </div>
  );
}
