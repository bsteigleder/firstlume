import { useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router";
import { api as rootAPI, settingsPath, type CreateAgentResponse } from "../api";
import { Logo } from "../icons";
import { PageTitle, rememberOrg, useCrumbs, useOrg } from "../org";
import { Empty, ErrorBanner, slugify, timeAgo, useAction, useData } from "../ui";
import { SettingsEditor } from "./SettingsEditor";

export function FolderSettingsPage() {
  const { folderId = "" } = useParams();
  const { org, link, folderChain } = useOrg();
  const chain = folderChain(folderId);
  const folder = chain[chain.length - 1];
  useCrumbs([{ label: org.name, to: link() }, ...chain.map((f) => ({ label: f.name, to: link(`f/${f.id}`) })), { label: "Settings" }]);
  if (!folder) return null;
  return (
    <div className="page narrow">
      <PageTitle title={`${folder.name} · settings`} />
      <SettingsEditor level={{ kind: "folder", id: folder.id, name: folder.name }} path={settingsPath({ kind: "folder", id: folder.id })} />
    </div>
  );
}

export function OrgSettingsPage() {
  const { org, link } = useOrg();
  useCrumbs([{ label: org.name, to: link() }, { label: "Settings" }]);
  const sections = ["General", "Variables & settings", "Git connection", "Agents", "Members"];
  return (
    <div className="page settings-layout">
      <nav aria-label="Settings sections" className="settings-nav">
        <h1>{org.name}</h1>
        {sections.map((s) => (
          <a key={s} href={`#${slugify(s)}`}>
            {s}
          </a>
        ))}
      </nav>
      <div className="settings-main">
        <General />
        <section id="variables-settings">
          <h2 className="section-title">Variables &amp; settings</h2>
          <p className="muted">The organization is the last level a stack looks at: anything set in a folder or on the stack wins.</p>
          <SettingsEditor level={{ kind: "organization", id: org.id, name: org.name }} path={settingsPath({ kind: "organization" })} />
        </section>
        <section id="git-connection" className="card">
          <h2 className="section-title">Git connection</h2>
          <p className="muted">
            Connecting a Gitea account lets Firstlume list its repositories and find <span className="mono">.firstlume</span> folders for you. It comes
            in the next milestone; until then, stacks are created by typing the repository URL.
          </p>
        </section>
        <Agents />
        <section id="members" className="card">
          <h2 className="section-title">Members</h2>
          <p className="muted">
            Sign-in with Gitea and invitations by Gitea username come with the login milestone. For now Firstlume is used with the admin token, which can
            do everything.
          </p>
        </section>
      </div>
    </div>
  );
}

function General() {
  const { org, api } = useOrg();
  const [name, setName] = useState(org.name);
  const { busy, error, run } = useAction();
  return (
    <form
      id="general"
      className="card form-card"
      onSubmit={(e) => {
        e.preventDefault();
        void run(async () => {
          await api.rename(name);
          window.location.reload();
        });
      }}
    >
      <h2 className="section-title">General</h2>
      <div className="grid-2">
        <label className="field">
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        <label className="field">
          Address
          <input className="mono" value={org.slug} disabled />
          <span className="hint">The address can’t be changed.</span>
        </label>
      </div>
      {error && <div className="form-error">{error}</div>}
      <div className="form-actions">
        <button className="primary" disabled={busy || name.trim() === org.name || !name.trim()}>
          Save
        </button>
      </div>
    </form>
  );
}

// An agent polls every few seconds; one not seen for this long is probably down.
const onlineWindowMs = 60_000;

function Agents() {
  const { api } = useOrg();
  const agents = useData(() => api.listAgents(), [api], { poll: true, pollMs: 10000 });
  const [name, setName] = useState("");
  const [created, setCreated] = useState<CreateAgentResponse | null>(null);
  const action = useAction();

  async function create(e: FormEvent) {
    e.preventDefault();
    await action.run(async () => {
      setCreated(await api.createAgent(name));
      setName("");
      await agents.reload();
    });
  }

  return (
    <section id="agents" className="card">
      <div className="section-head">
        <h2 className="section-title">Agents</h2>
      </div>
      <p className="muted">Agents pull runs and execute them in their own cluster or network.</p>
      <ErrorBanner error={agents.error ?? action.error} onDismiss={action.error ? () => action.setError(null) : undefined} />
      {created && (
        <div className="token-card">
          <strong>Agent “{created.agent.name}” created. Copy its token now — it is shown only once.</strong>
          <div className="token-row">
            <code className="mono">{created.token}</code>
            <button type="button" onClick={() => void navigator.clipboard?.writeText(created.token)}>
              Copy
            </button>
          </div>
          <span className="muted small">
            Install it with Helm: <span className="mono">--set agent.token=…</span>
          </span>
          <button className="link fit" onClick={() => setCreated(null)}>
            Done
          </button>
        </div>
      )}
      <form className="inline-form" onSubmit={create}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="New agent name, e.g. zone-sp2" aria-label="New agent name" />
        <button className="primary" disabled={!name || action.busy}>
          New agent
        </button>
      </form>
      {agents.data &&
        (agents.data.length === 0 ? (
          <Empty>No agents registered.</Empty>
        ) : (
          <table className="table plain-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Last seen</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {agents.data.map((a) => {
                const online = a.last_seen_at && Date.now() - new Date(a.last_seen_at).getTime() < onlineWindowMs;
                return (
                  <tr key={a.id}>
                    <td>
                      <strong>{a.name}</strong>
                    </td>
                    <td>
                      <span className={`badge ${online ? "ok" : "idle"}`}>{online ? "Online" : a.last_seen_at ? "Offline" : "Waiting for first contact"}</span>
                    </td>
                    <td className="muted">{timeAgo(a.last_seen_at)}</td>
                    <td className="right">
                      <button
                        className="small-btn"
                        disabled={action.busy}
                        onClick={() => {
                          if (!confirm(`Delete agent “${a.name}”? It will stop picking up runs.`)) return;
                          void action.run(async () => {
                            await api.deleteAgent(a.id);
                            await agents.reload();
                          });
                        }}
                      >
                        Delete
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ))}
    </section>
  );
}

/** Creates an organization; with `welcome`, it is the first screen of a user without any. */
export function CreateOrgPage({ welcome }: { welcome?: boolean }) {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const { busy, error, run } = useAction();
  const address = slugTouched ? slug : slugify(name);

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const o = await rootAPI.createOrg(address, name.trim());
      rememberOrg(o.slug);
      navigate(`/o/${o.slug}`);
    });
  }

  return (
    <div className="welcome">
      <header className="welcome-bar">
        <Logo />
        <strong>Firstlume</strong>
      </header>
      <main className="welcome-main">
        <h1>{welcome ? "Welcome" : "Create organization"}</h1>
        <p className="lead">
          {welcome
            ? "You don’t belong to any organization yet, so let’s create your first one."
            : "An organization has its own folders, stacks, settings and agents. Nothing is shared between organizations."}
        </p>
        <form className="card form-card welcome-card" onSubmit={submit}>
          <label className="field">
            Name
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus required placeholder="Acme" />
          </label>
          <label className="field">
            Address
            <span className="prefixed">
              <span className="mono prefix">{window.location.host}/o/</span>
              <input className="mono" value={address} onChange={(e) => (setSlug(e.target.value), setSlugTouched(true))} required placeholder="acme" />
            </span>
            <span className="hint">Lowercase letters, digits and hyphens. Suggested from the name.</span>
          </label>
          {error && <div className="form-error">{error}</div>}
          <div className="form-actions start">
            <button className="primary" disabled={busy || !name.trim() || !address}>
              {busy ? "Creating…" : "Create organization"}
            </button>
            {!welcome && (
              <button type="button" onClick={() => navigate(-1)}>
                Cancel
              </button>
            )}
          </div>
        </form>
        {welcome && <p className="hint">Next you’ll create your first stack from a repository with a .firstlume folder.</p>}
      </main>
    </div>
  );
}
