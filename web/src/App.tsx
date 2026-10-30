import { useEffect, useState, type FormEvent } from "react";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router";
import { api, getToken, setToken, setUnauthorizedHandler } from "./api";
import { Logo } from "./icons";
import { lastOrg, OrgShell } from "./org";
import { FolderPage } from "./pages/FolderPage";
import { RunPage } from "./pages/RunPage";
import { CreateOrgPage, FolderSettingsPage, OrgSettingsPage } from "./pages/SettingsPages";
import { StackPage } from "./pages/StackPage";
import { ThemeSwitch } from "./theme";
import { ErrorBanner, errorMessage, useData } from "./ui";

const router = createBrowserRouter([
  { index: true, element: <OrgIndex /> },
  { path: "new-org", element: <CreateOrgPage /> },
  {
    path: "o/:org",
    element: <OrgShell />,
    children: [
      { index: true, element: <FolderPage /> },
      { path: "f/:folderId", element: <FolderPage /> },
      { path: "f/:folderId/settings", element: <FolderSettingsPage /> },
      { path: "s/:stackId", element: <StackPage tab="runs" /> },
      { path: "s/:stackId/settings", element: <StackPage tab="settings" /> },
      { path: "s/:stackId/source", element: <StackPage tab="source" /> },
      { path: "r/:runId", element: <RunPage /> },
      { path: "settings", element: <OrgSettingsPage /> },
    ],
  },
  { path: "*", element: <Navigate to="/" replace /> },
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

/** Opens the last organization used, the first one, or the welcome screen when there is none. */
function OrgIndex() {
  const orgs = useData(api.listOrgs, []);
  if (orgs.error) return <ErrorBanner error={orgs.error} />;
  if (!orgs.data) return null;
  if (orgs.data.length === 0) return <CreateOrgPage welcome />;
  const remembered = lastOrg();
  const target = orgs.data.find((o) => o.slug === remembered) ?? orgs.data[0];
  return <Navigate to={`/o/${target.slug}`} replace />;
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
      <main className="login-main">
        <div className="login-brand">
          <span className="login-logo">
            <Logo size={28} />
          </span>
          <h1>Sign in to Firstlume</h1>
          <p className="muted">Sign-in with Gitea comes with the login milestone. For now, use the admin token.</p>
        </div>
        <form className="card login-card" onSubmit={submit}>
          <label className="field">
            Admin token
            <input type="password" autoFocus autoComplete="current-password" value={token} onChange={(e) => setTok(e.target.value)} placeholder="fl_admin_…" />
          </label>
          {error && <div className="form-error">{error}</div>}
          <button className="primary" disabled={busy || token.trim() === ""}>
            {busy ? "Checking…" : "Sign in"}
          </button>
          <p className="hint">The server’s FIRSTLUME_ADMIN_TOKEN. In local development it is in .env.</p>
        </form>
      </main>
    </div>
  );
}
