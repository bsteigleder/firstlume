import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api as rootAPI, settingsPath, type CreateAgentResponse, type GitConnection, type Organization } from "../api";
import { Logo } from "../icons";
import { PageTitle, rememberOrg, useCrumbs, useOrg } from "../org";
import { useSession } from "../session";
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
        <GitConnectionSection />
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

function GitConnectionSection() {
  const { api } = useOrg();
  // undefined while loading, null when there is none.
  const [conn, setConn] = useState<GitConnection | null | undefined>(undefined);
  const [form, setForm] = useState({ url: "", account: "", token: "" });
  const [repos, setRepos] = useState<number | null>(null);
  const save = useAction();
  const remove = useAction();

  useEffect(() => {
    api.gitConnection().then(
      (c) => (setConn(c), setForm({ url: c.url, account: c.account, token: "" })),
      () => setConn(null),
    );
  }, [api]);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    await save.run(async () => {
      const c = await api.putGitConnection({ provider: "gitea", ...form });
      setConn(c);
      setRepos(c.repositories ?? null);
      setForm((f) => ({ ...f, token: "" }));
    });
  }

  return (
    <form id="git-connection" className="card form-card" onSubmit={submit}>
      <div className="section-head">
        <h2 className="section-title">Git connection</h2>
        {conn && <span className="badge ok">Connected{repos !== null ? ` · ${repos} repositories` : ""}</span>}
      </div>
      <p className="muted">
        Firstlume lists the account’s repositories, finds <span className="mono">.firstlume</span> folders and clones with a service token, not
        anyone’s personal login, so scheduled runs keep working when people leave.
      </p>
      {conn !== undefined && (
        <div className="grid-2">
          <label className="field">
            Provider
            <select disabled value="gitea">
              <option value="gitea">Gitea</option>
            </select>
          </label>
          <label className="field">
            Gitea URL
            <input type="url" value={form.url} onChange={set("url")} placeholder="https://git.example.com" required />
            {form.url.startsWith("http://") && <span className="hint warn-text">Over http the service token travels unencrypted: use https outside a local test.</span>}
          </label>
          <label className="field">
            Account <span className="optional">organization or user</span>
            <input value={form.account} onChange={set("account")} placeholder="infra" required />
          </label>
          <label className="field">
            Service token
            <input type="password" autoComplete="off" value={form.token} onChange={set("token")}
              placeholder={conn ? "•••••••• (saved, never shown)" : "token of a bot account"} required={!conn} />
            <span className="hint">From a bot account with read access to the repositories.</span>
          </label>
        </div>
      )}
      {save.error && <div className="form-error">{save.error}</div>}
      {remove.error && <div className="form-error">{remove.error}</div>}
      <div className="form-actions">
        {conn && (
          <button
            type="button"
            disabled={remove.busy}
            onClick={() => {
              if (!confirm("Disconnect Gitea? Stacks created from it must be deleted first.")) return;
              void remove.run(async () => {
                await api.deleteGitConnection();
                setConn(null);
                setRepos(null);
              });
            }}
          >
            Disconnect
          </button>
        )}
        <button className="primary" disabled={save.busy || !form.url || !form.account || (!conn && !form.token)}>
          {save.busy ? "Testing…" : conn ? "Test and save" : "Connect"}
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
/**
 * Creates an organization. As the welcome screen (first access, R7.7) it
 * greets a signed-in user and, until members arrive (D8), lists the
 * organizations that already exist.
 */
export function CreateOrgPage({ welcome, existing = [] }: { welcome?: boolean; existing?: Organization[] }) {
  const navigate = useNavigate();
  const { me, reload } = useSession();
  const user = me.user;
  const firstName = user?.name.split(" ")[0] || user?.login;
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
      // Leave "/" first: reloading /me while the welcome screen is mounted
      // re-renders it with the old organization list, which redirects away.
      // The reload makes /me list the new membership for the next visit.
      navigate(`/o/${o.slug}`);
      void reload();
    });
  }

  return (
    <div className="welcome">
      <header className="welcome-bar">
        <Logo />
        <strong>Firstlume</strong>
      </header>
      <main className="welcome-main">
        <h1>{welcome ? (firstName ? `Welcome, ${firstName}` : "Welcome") : "Create organization"}</h1>
        <p className="lead">
          {welcome
            ? "You don’t belong to any organization yet, so let’s create your first one. You’ll be its administrator."
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
        {welcome && existing.length > 0 && (
          <section className="card welcome-existing" aria-labelledby="existing-orgs">
            <h2 id="existing-orgs">Or open an existing organization</h2>
            <p className="muted small">Everyone who signs in sees every organization for now. Members and invitations come next.</p>
            <ul>
              {existing.map((o) => (
                <li key={o.slug}>
                  <Link to={`/o/${o.slug}`} onClick={() => rememberOrg(o.slug)}>
                    {o.name}
                  </Link>{" "}
                  <span className="mono muted small">{o.slug}</span>
                </li>
              ))}
            </ul>
          </section>
        )}

      </main>
    </div>
  );
}
