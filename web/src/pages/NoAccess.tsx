import { useState } from "react";
import { Logo } from "../icons";
import { Avatar, signOutAndLeave } from "../org";
import { useSession } from "../session";

/**
 * For someone signed in who isn't in any organization and isn't an installation
 * administrator: they need an invitation, and whoever invites needs their
 * username. It names no organization and no administrator (R7.17).
 */
export function NoAccessPage({ onCheck }: { onCheck: () => Promise<void> }) {
  const { me } = useSession();
  const [checking, setChecking] = useState(false);
  const [checked, setChecked] = useState(false);
  const [copied, setCopied] = useState(false);
  const login = me.user?.login ?? "";

  async function check() {
    setChecking(true);
    await onCheck();
    setChecking(false);
    setChecked(true);
  }

  async function copy() {
    await navigator.clipboard?.writeText(login);
    setCopied(true);
  }

  return (
    <div className="welcome">
      <header className="welcome-bar">
        <Logo />
        <strong>First Lume</strong>
        <span className="no-access-who">
          <Avatar user={me.user} size={28} />
          {login} · via Gitea
        </span>
      </header>
      <main className="welcome-main no-access">
        <h1>You don’t have access yet</h1>
        <p className="lead">Your account isn’t in any organization. An administrator needs to invite you, and they’ll need your Gitea username.</p>
        <section className="card welcome-card no-access-card" aria-labelledby="no-access-username">
          <div className="no-access-username">
            <h2 id="no-access-username">Your Gitea username</h2>
            <div className="token-row">
              <code className="mono">{login}</code>
              <button type="button" onClick={() => void copy()}>
                {copied ? "Copied" : "Copy"}
              </button>
            </div>
          </div>
          <div className="no-access-check">
            <button className="primary" disabled={checking} onClick={() => void check()}>
              {checking ? "Checking…" : "Check again"}
            </button>
            {checked && !checking && (
              <span role="status" className="muted small">
                No invitation yet. Checked just now.
              </span>
            )}
          </div>
        </section>
        <p className="hint">
          Once you’re invited, your organizations appear here without signing in again. Wrong account?{" "}
          <button className="link" onClick={() => void signOutAndLeave(me)}>
            Sign out
          </button>
        </p>
      </main>
    </div>
  );
}
