import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router";
import { api, authConfig, loginURL, setToken, setUnauthorizedHandler, type AuthConfig, type Me } from "./api";
import { Logo } from "./icons";
import { lastOrg, OrgShell } from "./org";
import { FolderPage } from "./pages/FolderPage";
import { GitEventsPage } from "./pages/GitEvents";
import { NoAccessPage } from "./pages/NoAccess";
import { RunPage } from "./pages/RunPage";
import { CreateOrgPage, FolderSettingsPage, OrgSettingsPage } from "./pages/SettingsPages";
import { StackPage } from "./pages/StackPage";
import { SessionContext, useSession } from "./session";
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
      { path: "s/:stackId/resources", element: <StackPage tab="resources" /> },
      { path: "s/:stackId/settings", element: <StackPage tab="settings" /> },
      { path: "s/:stackId/source", element: <StackPage tab="source" /> },
      { path: "r/:runId", element: <RunPage /> },
      { path: "settings", element: <OrgSettingsPage /> },
      { path: "git-events", element: <GitEventsPage /> },
    ],
  },
  { path: "*", element: <Navigate to="/" replace /> },
]);

/** Signed-in state: unknown while /me answers, then a user or the admin token, or nobody. */
type Auth = { state: "loading" } | { state: "anon" } | { state: "in"; me: Me };

export function App() {
  const [auth, setAuth] = useState<Auth>({ state: "loading" });

  // The admin token (sessionStorage) or the session cookie: /me says which, if any.
  const load = useCallback(async () => {
    try {
      setAuth({ state: "in", me: await api.me() });
    } catch {
      setAuth({ state: "anon" });
    }
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setToken(null);
      setAuth({ state: "anon" });
    });
    void load();
  }, [load]);

  if (auth.state === "loading") return null;
  if (auth.state === "anon") return <Login onLogin={load} />;
  return (
    <SessionContext.Provider value={{ me: auth.me, reload: load }}>
      <RouterProvider router={router} />
    </SessionContext.Provider>
  );
}

/**
 * Opens the last organization used, or the first one the caller can see. With
 * none, an installation administrator lands on the welcome screen to create one
 * (R7.7) and anyone else on the screen that asks for an invitation (R7.17).
 */
function OrgIndex() {
  const { me, reload } = useSession();
  const orgs = useData(api.listOrgs, [], { poll: true, pollMs: 15000 });
  if (orgs.error) return <ErrorBanner error={orgs.error} />;
  if (!orgs.data) return null;
  if (orgs.data.length === 0) {
    if (me.installation_admin) return <CreateOrgPage welcome />;
    return <NoAccessPage onCheck={() => Promise.all([orgs.reload(), reload()]).then(() => undefined)} />;
  }
  const remembered = lastOrg();
  const target = orgs.data.find((o) => o.slug === remembered) ?? orgs.data[0];
  return <Navigate to={`/o/${target.slug}`} replace />;
}

type Tone = "error" | "attention" | "success";

/** The message for ?login_error= or ?signed_out, set by the server or by signing out (R7.14). */
function loginMessage(search: string, provider: string): { tone: Tone; title: string; text: string; hint?: string } | null {
  const q = new URLSearchParams(search);
  if (q.has("signed_out")) {
    return { tone: "success", title: "You’re signed out of First Lume.", text: `Your ${provider} session stays open.` };
  }
  switch (q.get("login_error")) {
    case null:
      return null;
    case "denied":
      return {
        tone: "error",
        title: `Your ${provider} account can’t sign in here.`,
        text: `First Lume is open to members of certain ${provider} organizations or teams. Ask an administrator to add you.`,
        hint: `Signed in with another ${provider} account? Sign out of ${provider} first.`,
      };
    case "state":
      return { tone: "attention", title: "That sign-in didn’t finish.", text: "It took too long or the page was reloaded. Start again." };
    case "unavailable":
      return { tone: "attention", title: `${provider} wasn’t answering.`, text: "Try again in a moment." };
    default:
      return {
        tone: "error",
        title: `${provider} didn’t complete the sign-in.`,
        text: "Try again. If it keeps happening, an administrator can see why in the server log.",
      };
  }
}

function Message({ tone, title, children }: { tone: Tone; title: string; children: ReactNode }) {
  return (
    <div role={tone === "success" ? "status" : "alert"} className={`login-msg login-msg-${tone}`}>
      <span>
        <strong>{title}</strong> {children}
      </span>
    </div>
  );
}

function Login({ onLogin }: { onLogin: () => Promise<void> }) {
  const [cfg, setCfg] = useState<AuthConfig | null>(null);
  const [cfgError, setCfgError] = useState<string | null>(null);

  useEffect(() => {
    authConfig().then(setCfg, (err) => setCfgError(errorMessage(err)));
  }, []);

  // Read once: the query is cleared below so a reload doesn't show it again.
  const [search] = useState(() => window.location.search);
  const message = loginMessage(search, cfg?.oidc?.name ?? "Gitea");
  useEffect(() => {
    if (window.location.search) window.history.replaceState(null, "", window.location.pathname);
  }, []);

  if (!cfg && !cfgError) return null;
  const oidc = cfg?.oidc;
  const tokenLogin = cfgError !== null || (cfg?.admin_token_login ?? true);
  const returnTo = window.location.pathname === "/" ? "/" : window.location.pathname;

  return (
    <div className="login">
      <div className="login-theme">
        <ThemeSwitch />
      </div>
      <main className="login-main">
        <div className="login-brand">
          <span className="login-logo">
            <Logo size={72} />
          </span>
          <h1>Sign in to First Lume</h1>
          <p className="muted">{oidc ? "Use your Git account. You will come back here signed in." : "Use the admin token."}</p>
        </div>
        {message && (
          <Message tone={message.tone} title={message.title}>
            {message.text}
          </Message>
        )}
        {cfgError && <div className="form-error">Couldn’t read the sign-in options: {cfgError}</div>}
        <section className="card login-card">
          {oidc &&
            (oidc.available ? (
              <a className="button login-provider" href={loginURL(returnTo)}>
                Continue with {oidc.name}
              </a>
            ) : (
              <>
                <button className="login-provider" disabled>
                  Continue with {oidc.name}
                </button>
                <p className="hint">{oidc.name} isn’t answering. First Lume checks again every minute.</p>
              </>
            ))}
          {oidc?.available && message?.hint && <p className="hint">{message.hint}</p>}
          {oidc && tokenLogin && (
            <div className="login-or" aria-hidden>
              or
            </div>
          )}
          {tokenLogin &&
            (oidc ? (
              <details className="login-token" open={!oidc.available}>
                <summary>Use an admin token</summary>
                <TokenForm onLogin={onLogin} />
              </details>
            ) : (
              <TokenForm onLogin={onLogin} autoFocus />
            ))}
          {!oidc && !tokenLogin && (
            <div className="login-unset">
              <strong>Sign-in isn’t set up yet</strong>
              <p className="muted">An administrator turns on Gitea sign-in when installing First Lume, in the Helm values:</p>
              <pre className="mono">{"controlPlane:\n  publicURL: https://firstlume.example.com\n  auth:\n    oidc:\n      enabled: true\n      issuer: https://gitea.example.com"}</pre>
            </div>
          )}
        </section>
        {oidc?.available && (
          <p className="notice">
            <strong>First time here?</strong> Sign in with {oidc.name} and you’ll create your organization right after.
          </p>
        )}
      </main>
    </div>
  );
}

function TokenForm({ onLogin, autoFocus }: { onLogin: () => Promise<void>; autoFocus?: boolean }) {
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
      await onLogin();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="login-token-form" onSubmit={submit}>
      <label className="field">
        Admin token
        <input type="password" autoFocus={autoFocus} autoComplete="current-password" value={token} onChange={(e) => setTok(e.target.value)} placeholder="fl_admin_…" />
      </label>
      {error && <div className="form-error">{error}</div>}
      <button className={autoFocus ? "primary" : undefined} disabled={busy || token.trim() === ""}>
        {busy ? "Checking…" : "Sign in with token"}
      </button>
      <p className="hint">For automation and emergencies. Your administrator can turn this off.</p>
    </form>
  );
}
