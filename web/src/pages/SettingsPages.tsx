import { useEffect, useState, type FormEvent } from "react";
import { Navigate, useNavigate, useParams } from "react-router";
import { api as rootAPI, settingsPath, type CreateAgentResponse, type GitConnection, type VaultConnection } from "../api";
import { Logo } from "../icons";
import { PageTitle, rememberOrg, useCrumbs, useOrg } from "../org";
import { useSession } from "../session";
import { Empty, ErrorBanner, slugify, timeAgo, useAction, useData } from "../ui";
import { Members } from "./Members";
import { SettingsEditor } from "./SettingsEditor";
import { declaredVaultPaths, VaultAccessCheck } from "./VaultParts";

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
  const sections = ["General", "Variables & settings", "Git connection", "Vault", "Agents", "Members"];
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
        <VaultSection />
        <Agents />
        <Members />
      </div>
    </div>
  );
}

function General() {
  const { org, api } = useOrg();
  const [name, setName] = useState(org.name);
  const [slug, setSlug] = useState(org.slug);
  const { busy, error, run } = useAction();
  const slugChanged = slug.trim() !== org.slug;
  return (
    <form
      id="general"
      className="card form-card"
      onSubmit={(e) => {
        e.preventDefault();
        void run(async () => {
          const o = await api.update(name.trim(), slug.trim());
          rememberOrg(o.slug);
          // A full load, so the organization list and every link use the new address.
          window.location.assign(window.location.pathname.replace(`/o/${org.slug}`, `/o/${o.slug}`) + window.location.hash);
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
          <span className="prefixed">
            <span className="mono prefix">{window.location.host}/o/</span>
            <input className="mono" value={slug} onChange={(e) => setSlug(e.target.value)} required placeholder="acme" />
          </span>
          {slugChanged ? (
            <span className="hint warn-text">
              Links to <span className="mono">/o/{org.slug}</span> will keep working. Vault roles that check the <span className="mono">org</span> claim
              must be updated to <span className="mono">{slug.trim() || "…"}</span>.
            </span>
          ) : (
            <span className="hint">Lowercase letters, digits and hyphens.</span>
          )}
        </label>
      </div>
      {error && <div className="form-error">{error}</div>}
      <div className="form-actions">
        <button className="primary" disabled={busy || !name.trim() || !slug.trim() || (name.trim() === org.name && !slugChanged)}>
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

function VaultSection() {
  const { api, tree } = useOrg();
  // undefined while loading, null when there is none.
  const [conn, setConn] = useState<VaultConnection | null | undefined>(undefined);
  const [form, setForm] = useState({ address: "", namespace: "", auth_path: "jwt", audience: "vault", ca_cert: "" });
  const [publicKey, setPublicKey] = useState("");
  const [issuer, setIssuer] = useState("");
  const [stackID, setStackID] = useState("");
  const [copied, setCopied] = useState(false);
  const save = useAction();
  const remove = useAction();

  useEffect(() => {
    api.vaultConnection().then(
      (c) => (setConn(c), setForm({ address: c.address, namespace: c.namespace ?? "", auth_path: c.auth_path, audience: c.audience, ca_cert: c.ca_cert ?? "" })),
      () => setConn(null),
    );
    api.vaultPublicKey().then(setPublicKey, () => setPublicKey(""));
    fetch("/.well-known/openid-configuration")
      .then((r) => r.json())
      .then((d) => setIssuer(d.issuer ?? ""), () => setIssuer(""));
  }, [api]);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const stacks = (tree?.stacks ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
  const selected = stacks.find((s) => s.id === stackID);

  async function submit(e: FormEvent) {
    e.preventDefault();
    await save.run(async () => setConn(await api.putVaultConnection(form)));
  }

  return (
    <section id="vault" className="settings-group">
      <form className="card form-card" onSubmit={submit}>
        <div className="section-head">
          <h2 className="section-title">Vault</h2>
          {conn && <span className="badge ok">Connected</span>}
        </div>
        <p className="muted">
          Runs read secrets straight from Vault, inside their own pod: values never pass through Firstlume. Each phase logs in with a short-lived token
          Firstlume signs for its stack, and the stack’s Vault role (a setting, inherited like the others) decides what it may read.
        </p>
        {conn !== undefined && (
          <div className="grid-2">
            <label className="field">
              Address
              <input type="url" value={form.address} onChange={set("address")} placeholder="https://vault.example.com:8200" required />
            </label>
            <label className="field">
              Namespace <span className="optional">Vault Enterprise, optional</span>
              <input value={form.namespace} onChange={set("namespace")} />
            </label>
            <label className="field">
              Auth method path
              <input className="mono" value={form.auth_path} onChange={set("auth_path")} placeholder="jwt" />
            </label>
            <label className="field">
              Audience
              <input className="mono" value={form.audience} onChange={set("audience")} placeholder="vault" />
            </label>
            <label className="field span-2">
              CA certificate <span className="optional">optional, PEM, when Vault’s certificate isn’t publicly trusted</span>
              <textarea className="mono" rows={3} value={form.ca_cert} onChange={set("ca_cert")} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
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
                if (!confirm("Remove the Vault connection? Runs that read from Vault fail until there is one again.")) return;
                void remove.run(async () => {
                  await api.deleteVaultConnection();
                  setConn(null);
                });
              }}
            >
              Remove connection
            </button>
          )}
          <button className="primary" disabled={save.busy || !form.address}>
            {save.busy ? "Saving…" : "Save"}
          </button>
        </div>
      </form>

      <div className="card form-card">
        <h3>Make Vault trust Firstlume</h3>
        <p className="muted">
          Once, in Vault: enable the <span className="mono">jwt</span> auth method with Firstlume as the issuer and its public key, or its discovery URL
          when Vault can reach Firstlume.
        </p>
        <div className="grid-2">
          <div className="field">
            Issuer <span className="optional">bound_issuer, oidc_discovery_url</span>
            <code className="mono code-block">{issuer || "…"}</code>
          </div>
          <div className="field">
            JWKS
            <code className="mono code-block">{issuer ? `${issuer}/.well-known/jwks.json` : "…"}</code>
          </div>
        </div>
        <div className="field">
          <div className="section-head">
            <span>Public key <span className="optional">jwt_validation_pubkeys</span></span>
            <button type="button" className="small-btn" disabled={!publicKey}
              onClick={() => void navigator.clipboard.writeText(publicKey).then(() => (setCopied(true), setTimeout(() => setCopied(false), 1500)))}>
              {copied ? "Copied" : "Copy"}
            </button>
            {publicKey && (
              <a className="small-btn" download="firstlume-run-jwt.pem" href={`data:application/x-pem-file,${encodeURIComponent(publicKey)}`}>
                Download .pem
              </a>
            )}
          </div>
          <pre className="mono code-block key-box">{publicKey || "…"}</pre>
        </div>
        <details>
          <summary>Example: a role for the stacks under a folder infra/dev</summary>
          <pre className="mono code-block">{`vault auth enable jwt
vault write auth/jwt/config bound_issuer=${issuer || "<issuer>"} jwt_validation_pubkeys=@firstlume-run-jwt.pem
vault write auth/jwt/role/infra-dev role_type=jwt user_claim=sub \\
  bound_audiences=${form.audience || "vault"} bound_claims_type=glob \\
  bound_claims='{"stack_path": "infra/dev/*"}' \\
  token_policies=infra-dev token_ttl=15m`}</pre>
          <p className="hint">
            A run’s token carries <span className="mono">stack_path</span> (folders and stack name), <span className="mono">folder_ids</span>,{" "}
            <span className="mono">stack_id</span>, <span className="mono">org</span> and <span className="mono">phase</span> (plan, apply or drift).
            Prefer <span className="mono">folder_ids</span> if renaming a folder must not change what it can read.
          </p>
        </details>
      </div>

      {conn && (
        <div className="card form-card">
          <h3>Test a stack’s access</h3>
          <p className="muted">Logs in as the stack would, with its Vault role, and asks Vault what it may read. Nothing is read.</p>
          <label className="field">
            Stack
            <select value={stackID} onChange={(e) => setStackID(e.target.value)}>
              <option value="">Choose a stack</option>
              {stacks.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} · {s.path || "/"}
                </option>
              ))}
            </select>
          </label>
          {selected && <VaultAccessCheck stackID={selected.id} initialPaths={declaredVaultPaths(selected.config?.declaration).fixed} />}
        </div>
      )}
    </section>
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
 * Creates an organization, for installation administrators only (R7.16). As the
 * welcome screen (first access, R7.7) it greets a signed-in user.
 */
export function CreateOrgPage({ welcome }: { welcome?: boolean }) {
  const navigate = useNavigate();
  const { me, reload } = useSession();
  const user = me.user;
  const firstName = user?.name.split(" ")[0] || user?.login;
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const { busy, error, run } = useAction();
  const address = slugTouched ? slug : slugify(name);

  if (!me.installation_admin) return <Navigate to="/" replace />;

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
      </main>
    </div>
  );
}
