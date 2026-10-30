import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { createBrowserRouter, Navigate, NavLink, Outlet, RouterProvider } from "react-router";
import { api, getToken, setToken, setUnauthorizedHandler } from "./api";
import { errorMessage } from "./ui";
import { useTheme, type Theme } from "./theme";
import { EnvironmentsPage } from "./pages/Environments";
import { EnvironmentPage } from "./pages/Environment";
import { RunPage } from "./pages/Run";
import { TemplatesPage } from "./pages/Templates";
import { AgentsPage } from "./pages/Agents";

const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { index: true, element: <Navigate to="/environments" replace /> },
      { path: "environments", element: <EnvironmentsPage /> },
      { path: "environments/:id", element: <EnvironmentPage /> },
      { path: "runs/:id", element: <RunPage /> },
      { path: "templates", element: <TemplatesPage /> },
      { path: "agents", element: <AgentsPage /> },
      { path: "*", element: <Navigate to="/environments" replace /> },
    ],
  },
]);

export function App() {
  const [authed, setAuthed] = useState(() => getToken() !== null);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setToken(null);
      setAuthed(false);
    });
  }, []);

  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  return <RouterProvider router={router} />;
}

function Layout() {
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <Logo />
          <span>Firstlume</span>
        </div>
        <nav>
          <NavLink to="/environments">Environments</NavLink>
          <NavLink to="/templates">Templates</NavLink>
          <NavLink to="/agents">Agents</NavLink>
        </nav>
        <div className="sidebar-footer">
          <ThemeSwitch />
          <button
            className="link"
            onClick={() => {
              setToken(null);
              window.location.assign("/");
            }}
          >
            Sign out
          </button>
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}

function Login({ onLogin }: { onLogin: () => void }) {
  const [token, setTok] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.checkToken(token.trim());
      setToken(token.trim());
      onLogin();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <div className="login-theme">
        <ThemeSwitch />
      </div>
      <form className="card login-card" onSubmit={submit}>
        <div className="brand brand-large">
          <Logo />
          <span>Firstlume</span>
        </div>
        <label>
          Admin token
          <input
            type="password"
            autoFocus
            autoComplete="current-password"
            value={token}
            onChange={(e) => setTok(e.target.value)}
            placeholder="FIRSTLUME_ADMIN_TOKEN"
          />
        </label>
        {error && <div className="form-error">{error}</div>}
        <button className="primary" disabled={busy || token.trim() === ""}>
          {busy ? "Checking…" : "Sign in"}
        </button>
        <p className="hint">Use the server's FIRSTLUME_ADMIN_TOKEN. In local dev it is in .env.</p>
      </form>
    </div>
  );
}

const themeOptions: { value: Theme; label: string; icon: ReactNode }[] = [
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

function ThemeSwitch() {
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

function Logo() {
  return (
    <svg className="logo" viewBox="0 0 24 24" aria-hidden>
      <circle cx="12" cy="12" r="4" fill="currentColor" />
      <g stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
        <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" />
      </g>
    </svg>
  );
}
