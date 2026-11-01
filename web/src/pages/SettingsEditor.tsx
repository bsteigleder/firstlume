import { useEffect, useState } from "react";
import { Link } from "react-router";
import type { EffectiveVariable, Level, LevelSettings, Variable } from "../api";
import { useOrg } from "../org";
import { driftIntervals, driftLabel, ErrorBanner, levelName, useAction, useData } from "../ui";

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
  const [dirty, setDirty] = useState(false);
  const save = useAction();

  useEffect(() => {
    const d = settings.data;
    if (!d) return;
    setApproval(toTri(d.level.require_approval));
    setConfirmDestructive(toTri(d.level.confirm_destructive));
    setDrift(d.level.drift_interval_minutes);
    setVars(d.level.variables.map((v) => ({ ...v, value: v.sensitive ? "" : v.value, stored: v.sensitive })));
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
    setVars((vs) => [...vs, { key: v.key, category: v.category, sensitive: v.sensitive, value: v.sensitive ? "" : v.value }]);
    touch();
  }

  async function submit() {
    const body: LevelSettings = {
      require_approval: fromTri(approval),
      confirm_destructive: fromTri(confirmDestructive),
      drift_interval_minutes: drift,
      // Edited elsewhere for now (spec 0003, F4): kept as it is.
      vault_role: settings.data?.level.vault_role ?? null,
      variables: vars.map(({ key, category, sensitive, value }) => ({ key: key.trim(), category, sensitive, value: value ?? "" })),
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
      </section>

      <section className="vars-section">
        <div className="section-head">
          <h2>Variables &amp; secrets</h2>
          <span className="badge here">Set here</span>
          <span className="badge idle">Inherited</span>
          <button onClick={() => (setVars((vs) => [...vs, { key: "", category: "terraform", sensitive: false, value: "" }]), touch())}>Add variable</button>
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
                      <input className="mono" value={v.key} onChange={(e) => update(i, { key: e.target.value })} placeholder="name" aria-label="Variable name" />
                    </td>
                    <td>
                      <input className="mono" type={v.sensitive ? "password" : "text"} value={v.value ?? ""} autoComplete="off" aria-label="Value"
                        placeholder={v.stored ? "•••••• (unchanged)" : "value"} onChange={(e) => update(i, { value: e.target.value })} />
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
                  <td className="mono">{v.sensitive ? "••••••••" : v.value}</td>
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
        <p className="hint">Secret values are never shown again after saving. Stacks below use the nearest level that sets each variable.</p>
      </section>

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

function SettingRow({ title, help, children }: { title: string; help: string; children: React.ReactNode }) {
  return (
    <div className="setting">
      <div>
        <strong>{title}</strong>
        <p className="muted">{help}</p>
      </div>
      {children}
    </div>
  );
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
