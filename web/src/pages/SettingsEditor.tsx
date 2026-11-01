import { useEffect, useState } from "react";
import { Link } from "react-router";
import type { EffectiveRegistry, EffectiveVariable, Level, LevelSettings, Variable } from "../api";
import { useOrg } from "../org";
import { driftIntervals, driftLabel, ErrorBanner, levelName, useAction, useData } from "../ui";
import { FileDialog, RegistryDialog, type FileDraft, type RegistryDraft } from "./SettingsDialogs";

type Tri = "inherit" | "yes" | "no";

const toTri = (v: boolean | null): Tri => (v === null ? "inherit" : v ? "yes" : "no");
const fromTri = (t: Tri): boolean | null => (t === "inherit" ? null : t === "yes");

interface EditableVariable extends Variable {
  // A sensitive variable already stored here, whose value is never sent back.
  stored?: boolean;
}

/**
 * Edits what one level (organization, folder or stack) defines, showing what
 * it inherits and from where. Stacks use the nearest level that defines each setting.
 */
export function SettingsEditor({ level, path }: { level: Level; path: string }) {
  const { api, link } = useOrg();
  const settings = useData(() => api.getSettings(path), [api, path]);
  const [approval, setApproval] = useState<Tri>("inherit");
  const [confirmDestructive, setConfirmDestructive] = useState<Tri>("inherit");
  const [drift, setDrift] = useState<number | null>(null);
  const [vars, setVars] = useState<EditableVariable[]>([]);
  const [vaultRole, setVaultRole] = useState("");
  const [regs, setRegs] = useState<RegistryDraft[]>([]);
  // The file or registry dialog, with the index it edits (-1 adds).
  const [dialog, setDialog] = useState<{ kind: "file" | "registry"; index: number } | null>(null);
  // An inherited credential being overridden: the dialog starts from it.
  const [pendingRegistry, setPendingRegistry] = useState<RegistryDraft | undefined>(undefined);
  const [dirty, setDirty] = useState(false);
  const save = useAction();

  useEffect(() => {
    const d = settings.data;
    if (!d) return;
    setApproval(toTri(d.level.require_approval));
    setConfirmDestructive(toTri(d.level.confirm_destructive));
    setDrift(d.level.drift_interval_minutes);
    setVars(d.level.variables.map((v) => ({ ...v, value: v.sensitive ? "" : v.value, stored: v.sensitive })));
    setVaultRole(d.level.vault_role ?? "");
    setRegs((d.level.registries ?? []).map((r) => ({ ...r, storedPassword: !!r.has_password && !r.password_vault })));
    setDirty(false);
  }, [settings.data]);

  if (settings.error && !settings.data) return <ErrorBanner error={settings.error} />;
  if (!settings.data) return null;
  const eff = settings.data.effective;
  const isHere = (o: Level) => o.kind === level.kind && (level.kind === "organization" || o.id === level.id);
  const inherited = eff.variables.filter((v) => !isHere(v.origin) && !vars.some((l) => l.key === v.key && l.category === v.category));
  const overridesOf = (v: Variable) => eff.variables.find((e) => e.key === v.key && e.category === v.category && isHere(e.origin))?.overrides;
  // "Yes, from Acme", "Off (default)", "Every 6 hours, set here".
  const describe = (value: string, origin: Level) =>
    origin.kind === "default" ? `${value} (default)` : isHere(origin) ? `${value}, set here` : `${value}, from ${levelName(origin)}`;

  const touch = () => setDirty(true);
  function update(i: number, patch: Partial<EditableVariable>) {
    setVars((vs) => vs.map((v, j) => (j === i ? { ...v, ...patch } : v)));
    touch();
  }
  function override(v: EffectiveVariable) {
    if (v.category === "file") {
      setDialog({ kind: "file", index: vars.length });
      setVars((vs) => [...vs, { key: v.key, category: "file", sensitive: v.sensitive, value: v.sensitive ? "" : v.value }]);
    } else {
      setVars((vs) => [...vs, { key: v.key, category: v.category, sensitive: v.sensitive, value: v.sensitive ? "" : v.value }]);
    }
    touch();
  }
  function saveFile(index: number, f: FileDraft) {
    const v: EditableVariable = { key: f.key, category: "file", sensitive: f.sensitive, value: f.stored ? "" : f.value, stored: f.stored };
    setVars((vs) => (index < 0 || index >= vs.length ? [...vs, v] : vs.map((x, j) => (j === index ? v : x))));
    setDialog(null);
    touch();
  }
  function saveRegistry(index: number, r: RegistryDraft) {
    setRegs((rs) => (index < 0 ? [...rs, r] : rs.map((x, j) => (j === index ? r : x))));
    setDialog(null);
    touch();
  }
  function overrideRegistry(r: EffectiveRegistry) {
    setDialog({ kind: "registry", index: -1 });
    setPendingRegistry({ host: r.host, kind: r.kind, username: r.username, password_vault: r.password_vault });
  }
  const inheritedRegs = eff.registries.filter((r) => !isHere(r.origin) && !regs.some((l) => l.host === r.host));
  const regOverrides = (host: string) => eff.registries.find((e) => e.host === host && isHere(e.origin))?.overrides;
  const levelLabel = level.name ?? levelName(level);

  async function submit() {
    const body: LevelSettings = {
      require_approval: fromTri(approval),
      confirm_destructive: fromTri(confirmDestructive),
      drift_interval_minutes: drift,
      // Edited elsewhere for now (spec 0003, F4): kept as it is.
      vault_role: vaultRole.trim() || null,
      variables: vars.map(({ key, category, sensitive, value }) => ({ key: key.trim(), category, sensitive, value: value ?? "" })),
      registries: regs.map(({ host, kind, username, password, password_vault }) => ({ host, kind, username, password, password_vault })),
    };
    if (await save.run(() => api.putSettings(path, body))) await settings.reload();
  }

  const triLabel = (current: { value: boolean; origin: Level }) => describe(current.value ? "Yes" : "No", current.origin);

  return (
    <div className="settings-editor">
      <Cascade level={level} />

      <section className="card settings-card">
        <SettingRow
          title="Require approval before apply"
          help={`Applies stop after the plan until someone approves them. Now: ${triLabel(eff.require_approval)}.`}
        >
          <TriSwitch label="Require approval" value={approval} onChange={(t) => (setApproval(t), touch())} />
        </SettingRow>
        <SettingRow
          title="Confirm each delete or replace"
          help={`Approvers must tick every resource the plan deletes or recreates. Off by default. Now: ${triLabel(eff.confirm_destructive)}.`}
        >
          <TriSwitch label="Confirm each delete or replace" value={confirmDestructive} onChange={(t) => (setConfirmDestructive(t), touch())} />
        </SettingRow>
        <SettingRow
          title="Drift detection"
          help={`Plans on a schedule and only reports. Now: ${describe(driftLabel(eff.drift_interval_minutes.value), eff.drift_interval_minutes.origin)}.`}
        >
          <select aria-label="Drift detection interval" value={drift === null ? "inherit" : String(drift)}
            onChange={(e) => (setDrift(e.target.value === "inherit" ? null : Number(e.target.value)), touch())}>
            <option value="inherit">Inherit</option>
            {driftIntervals.map((d) => (
              <option key={d.minutes} value={d.minutes}>
                {d.label}
              </option>
            ))}
          </select>
        </SettingRow>
        <SettingRow
          title="Vault role"
          help={`Runs log in to Vault with this role; Vault decides what it may read. Now: ${eff.vault_role.value ? describe(eff.vault_role.value, eff.vault_role.origin) : "none"}.`}
          link={<Link to={link("settings#vault")}>Vault connection and access test</Link>}
        >
          <label className="field compact">
            <span className="sr-only">Vault role</span>
            <input className="mono" value={vaultRole} onChange={(e) => (setVaultRole(e.target.value), touch())}
              placeholder={eff.vault_role.value && !isHere(eff.vault_role.origin) ? `inherit (${eff.vault_role.value})` : "inherit"} />
          </label>
        </SettingRow>
      </section>

      <section className="vars-section">
        <div className="section-head">
          <h2>Variables, secrets &amp; files</h2>
          <span className="badge here">Set here</span>
          <span className="badge idle">Inherited</span>
          <button onClick={() => (setVars((vs) => [...vs, { key: "", category: "terraform", sensitive: false, value: "" }]), touch())}>Add variable</button>
          <button onClick={() => setDialog({ kind: "file", index: -1 })}>Add file</button>
        </div>
        <div className="table-box">
          <table className="table vars">
            <thead>
              <tr>
                <th>Name</th>
                <th>Value</th>
                <th>Kind</th>
                <th>Secret</th>
                <th>Comes from</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {vars.map((v, i) => {
                const over = overridesOf(v);
                return (
                  <tr key={`l${i}`}>
                    <td>
                      <input className="mono" value={v.key} onChange={(e) => update(i, { key: e.target.value })} placeholder={v.category === "file" ? "path" : "name"}
                        aria-label={v.category === "file" ? "File path" : "Variable name"} />
                    </td>
                    <td>
                      {v.category === "file" ? (
                        <button className="small-btn" onClick={() => setDialog({ kind: "file", index: i })}>
                          {fileSummary(v)} · edit
                        </button>
                      ) : (
                        <input className="mono" type={v.sensitive ? "password" : "text"} value={v.value ?? ""} autoComplete="off" aria-label="Value"
                          placeholder={v.stored ? "•••••• (unchanged)" : "value"} onChange={(e) => update(i, { value: e.target.value })} />
                      )}
                    </td>
                    <td>
                      <select value={v.category} aria-label="Kind" onChange={(e) => update(i, { category: e.target.value as Variable["category"] })}>
                        <option value="terraform">Terraform</option>
                        <option value="env">Env var</option>
                        <option value="file">File</option>
                      </select>
                    </td>
                    <td>
                      <input type="checkbox" checked={v.sensitive} aria-label="Secret" onChange={(e) => update(i, { sensitive: e.target.checked, stored: false })} />
                    </td>
                    <td>
                      <span className="badge here">Set here</span>
                      {over && <div className="muted small">overrides {levelName(over)}</div>}
                    </td>
                    <td className="right">
                      <button className="link" onClick={() => (setVars((vs) => vs.filter((_, j) => j !== i)), touch())}>
                        Remove
                      </button>
                    </td>
                  </tr>
                );
              })}
              {inherited.map((v) => (
                <tr key={`i${v.category}/${v.key}`} className="inherited">
                  <td className="mono">{v.key}</td>
                  <td className="mono">{v.category === "file" ? fileSummary(v) : v.sensitive ? "••••••••" : v.value}</td>
                  <td>{categoryLabel[v.category]}</td>
                  <td>{v.sensitive ? "Yes" : ""}</td>
                  <td>
                    <OriginLink origin={v.origin} />
                    <div className="muted small">read-only here</div>
                  </td>
                  <td className="right">
                    <button className="small-btn" onClick={() => override(v)}>
                      Override
                    </button>
                  </td>
                </tr>
              ))}
              {vars.length === 0 && inherited.length === 0 && (
                <tr>
                  <td colSpan={6} className="muted">
                    No variables. Terraform variables reach the code as <span className="mono">TF_VAR_name</span>; env vars as they are.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <p className="hint">
          Secret values are never shown again after saving. Stacks below use the nearest level that sets each variable. Files are written into the
          stack’s folder before init; a secret file gets permission 0600. To generate files from Vault or one per machine, declare them in the
          repository’s <span className="mono">.firstlume/config.yaml</span>.
        </p>
      </section>

      <section className="vars-section">
        <div className="section-head">
          <h2>Registry credentials</h2>
          <button onClick={() => (setPendingRegistry(undefined), setDialog({ kind: "registry", index: -1 }))}>Add credential</button>
        </div>
        <div className="table-box">
          <table className="table vars">
            <thead>
              <tr>
                <th>Host</th>
                <th>Kind</th>
                <th>Username</th>
                <th>Password</th>
                <th>Comes from</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {regs.map((r, i) => {
                const over = regOverrides(r.host);
                return (
                  <tr key={`r${i}`}>
                    <td className="mono">{r.host}</td>
                    <td>{r.kind === "terraform" ? "Terraform" : "OCI"}</td>
                    <td className="mono">{r.username || "—"}</td>
                    <td><PasswordState r={r} /></td>
                    <td>
                      <span className="badge here">Set here</span>
                      {over && <div className="muted small">overrides {levelName(over)}</div>}
                    </td>
                    <td className="right nowrap">
                      <button className="link" onClick={() => setDialog({ kind: "registry", index: i })}>Edit</button>{" "}
                      <button className="link" onClick={() => (setRegs((rs) => rs.filter((_, j) => j !== i)), touch())}>Remove</button>
                    </td>
                  </tr>
                );
              })}
              {inheritedRegs.map((r) => (
                <tr key={`ir${r.host}`} className="inherited">
                  <td className="mono">{r.host}</td>
                  <td>{r.kind === "terraform" ? "Terraform" : "OCI"}</td>
                  <td className="mono">{r.username || "—"}</td>
                  <td><PasswordState r={r} /></td>
                  <td>
                    <OriginLink origin={r.origin} />
                    <div className="muted small">read-only here</div>
                  </td>
                  <td className="right">
                    <button className="small-btn" onClick={() => overrideRegistry(r)}>Override</button>
                  </td>
                </tr>
              ))}
              {regs.length === 0 && inheritedRegs.length === 0 && (
                <tr>
                  <td colSpan={6} className="muted">
                    No registry credentials. Add one for registries that need a login: Harbor, GHCR, Docker Hub or a private Terraform registry.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <p className="hint">
          Runs get OCI credentials as Docker’s <span className="mono">config.json</span> (Helm reads it too) and Terraform registries as{" "}
          <span className="mono">TF_TOKEN_&lt;host&gt;</span>. A repository can declare more hosts; for the same host, these settings win.
        </p>
      </section>

      {dialog?.kind === "file" && (
        <FileDialog
          levelName={levelLabel}
          draft={dialog.index >= 0 && dialog.index < vars.length ? fileDraft(vars[dialog.index]) : undefined}
          onSave={(f) => saveFile(dialog.index, f)}
          onClose={() => {
            // An override that never got content is dropped.
            if (dialog.index >= 0 && vars[dialog.index] && !vars[dialog.index].value && !vars[dialog.index].stored) {
              setVars((vs) => vs.filter((_, j) => j !== dialog.index));
            }
            setDialog(null);
          }}
        />
      )}
      {dialog?.kind === "registry" && (
        <RegistryDialog
          levelName={levelLabel}
          role={vaultRole || eff.vault_role.value}
          draft={dialog.index >= 0 ? regs[dialog.index] : pendingRegistry}
          onSave={(r) => saveRegistry(dialog.index, r)}
          onClose={() => setDialog(null)}
        />
      )}

      {save.error && <ErrorBanner error={save.error} onDismiss={() => save.setError(null)} />}
      <div className="form-actions">
        <button disabled={!dirty || save.busy} onClick={() => settings.reload()}>
          Discard changes
        </button>
        <button className="primary" disabled={!dirty || save.busy} onClick={submit}>
          {save.busy ? "Saving…" : "Save settings"}
        </button>
      </div>
      <p className="hint">
        Looking for the stack’s runs? Settings changes apply from the next plan; runs already planned keep what they ran with.{" "}
        {level.kind === "stack" && <Link to={link(`s/${level.id}`)}>Back to runs</Link>}
      </p>
    </div>
  );
}

function SettingRow({ title, help, link, children }: { title: string; help: string; link?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="setting">
      <div>
        <strong>{title}</strong>
        <p className="muted">{help}</p>
        {link && <p className="small">{link}</p>}
      </div>
      {children}
    </div>
  );
}

function PasswordState({ r }: { r: RegistryDraft | EffectiveRegistry }) {
  if (r.password_vault) {
    return (
      <span>
        <span className="badge vault">From Vault</span>
        <div className="mono small muted">{`${r.password_vault.path}#${r.password_vault.field}`}</div>
      </span>
    );
  }
  if (r.password) return <span className="badge here">New</span>;
  return <span className="badge idle">Saved</span>;
}

function fileDraft(v: EditableVariable): FileDraft {
  return { key: v.key, value: v.value ?? "", sensitive: v.sensitive, stored: v.stored };
}

function fileSummary(v: { value?: string; sensitive: boolean; stored?: boolean }): string {
  if (v.sensitive && !v.value) return v.stored || v.value === "" ? "•••••• secret" : "empty";
  const lines = (v.value ?? "").replace(/\n$/, "").split("\n").length;
  return v.value ? `${lines} line${lines === 1 ? "" : "s"}` : "empty";
}

function TriSwitch({ label, value, onChange }: { label: string; value: Tri; onChange: (t: Tri) => void }) {
  return (
    <div role="radiogroup" aria-label={label} className="segmented">
      {(["inherit", "yes", "no"] as Tri[]).map((t) => (
        <button key={t} type="button" role="radio" aria-checked={value === t} onClick={() => onChange(t)}>
          {t === "inherit" ? "Inherit" : t === "yes" ? "Yes" : "No"}
        </button>
      ))}
    </div>
  );
}

function OriginLink({ origin }: { origin: Level }) {
  const { link } = useOrg();
  const to = origin.kind === "organization" ? link("settings") : origin.kind === "folder" ? link(`f/${origin.id}/settings`) : origin.kind === "stack" ? link(`s/${origin.id}/settings`) : null;
  const label = levelName(origin);
  return to ? (
    <Link to={to} className="badge idle plain">
      {label}
    </Link>
  ) : (
    <span className="badge idle">{label}</span>
  );
}

/** "stack → prod → Ambientes → Acme": where values come from, nearest first. */
function Cascade({ level }: { level: Level }) {
  const { org, folderChain, tree } = useOrg();
  let folderID: string | undefined;
  if (level.kind === "folder") folderID = level.id;
  if (level.kind === "stack") folderID = tree?.stacks.find((s) => s.id === level.id)?.folder_id;
  const folders = folderChain(folderID).slice().reverse();
  const items = [
    ...(level.kind === "stack" ? [{ label: "this stack", current: true }] : level.kind === "folder" ? [] : [{ label: "stacks and folders", current: false }]),
    ...folders.map((f) => ({ label: f.name, current: level.kind === "folder" && f.id === level.id })),
    { label: org.name, current: level.kind === "organization" },
  ];
  if (level.kind === "folder") items.unshift({ label: "stacks", current: false });
  return (
    <div className="cascade">
      <span>Each stack uses the value from the nearest level:</span>
      <span className="cascade-chain">
        {items.map((it, i) => (
          <span key={i} className="cascade-item">
            {i > 0 && <span aria-hidden>→</span>}
            <span className={`chip${it.current ? " current" : ""}`}>{it.label}</span>
          </span>
        ))}
      </span>
    </div>
  );
}

const categoryLabel: Record<Variable["category"], string> = { terraform: "Terraform", env: "Env var", file: "File" };
