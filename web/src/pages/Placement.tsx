import { parse, stringify } from "yaml";
import type { Agent, Level, Placement, PlacementFieldName, Run, Toleration } from "../api";
import { placementFieldNames } from "../api";
import { levelName } from "../ui";

// Where runs execute (spec 0007, canvas boards 63-65). Node selector and
// tolerations have a form; the affinities are written in YAML, as in Kubernetes.

export const placementLabels: Record<PlacementFieldName, string> = {
  node_selector: "Node selector",
  tolerations: "Tolerations",
  node_affinity: "Node affinity",
  pod_affinity: "Pod affinity",
  pod_anti_affinity: "Pod anti-affinity",
};

const placementHelp: Record<PlacementFieldName, string> = {
  node_selector: "Only nodes with these labels.",
  tolerations: "Lets runs enter nodes with these taints. The taint itself stays on the node.",
  node_affinity: "Rules on node labels, required or preferred.",
  pod_affinity: "Run next to certain pods.",
  pod_anti_affinity: "Stay away from certain pods.",
};

const affinityFields = ["node_affinity", "pod_affinity", "pod_anti_affinity"] as const;

const affinityExample: Record<(typeof affinityFields)[number], string> = {
  node_affinity: `requiredDuringSchedulingIgnoredDuringExecution:
  nodeSelectorTerms:
    - matchExpressions:
        - key: topology.kubernetes.io/zone
          operator: In
          values: [east]
`,
  pod_affinity: `preferredDuringSchedulingIgnoredDuringExecution:
  - weight: 100
    podAffinityTerm:
      topologyKey: kubernetes.io/hostname
      labelSelector:
        matchLabels:
          app: my-app
`,
  pod_anti_affinity: `preferredDuringSchedulingIgnoredDuringExecution:
  - weight: 100
    podAffinityTerm:
      topologyKey: kubernetes.io/hostname
      labelSelector:
        matchLabels:
          app.kubernetes.io/managed-by: firstlume
`,
};

type Mode = "inherit" | "set" | "none";

export interface PlacementDraft {
  modes: Record<PlacementFieldName, Mode>;
  labels: { key: string; value: string }[];
  tolerations: Toleration[];
  affinity: Record<(typeof affinityFields)[number], string>;
}

const isEmpty = (v: unknown) => v == null || (Array.isArray(v) ? v.length === 0 : typeof v === "object" && Object.keys(v as object).length === 0);

export const placementIsZero = (p?: Placement | null) => !p || placementFieldNames.every((name) => p[name] === undefined || p[name] === null);

export function draftFrom(p: Placement = {}): PlacementDraft {
  const mode = (name: PlacementFieldName): Mode => (p[name] === undefined || p[name] === null ? "inherit" : isEmpty(p[name]) ? "none" : "set");
  const text = (name: (typeof affinityFields)[number]) => (isEmpty(p[name]) ? "" : stringify(p[name]));
  return {
    modes: {
      node_selector: mode("node_selector"),
      tolerations: mode("tolerations"),
      node_affinity: mode("node_affinity"),
      pod_affinity: mode("pod_affinity"),
      pod_anti_affinity: mode("pod_anti_affinity"),
    },
    labels: Object.entries(p.node_selector ?? {}).map(([key, value]) => ({ key, value })),
    tolerations: p.tolerations ?? [],
    affinity: { node_affinity: text("node_affinity"), pod_affinity: text("pod_affinity"), pod_anti_affinity: text("pod_anti_affinity") },
  };
}

/** The placement to send. It throws what is wrong with the YAML, naming the field. */
export function draftToPlacement(d: PlacementDraft): Placement {
  const out: Placement = {};
  if (d.modes.node_selector !== "inherit") {
    const labels = d.modes.node_selector === "set" ? d.labels.filter((l) => l.key.trim()) : [];
    out.node_selector = Object.fromEntries(labels.map((l) => [l.key.trim(), l.value.trim()]));
  }
  if (d.modes.tolerations !== "inherit") {
    out.tolerations = d.modes.tolerations === "set" ? d.tolerations.map(cleanToleration) : [];
  }
  for (const name of affinityFields) {
    if (d.modes[name] === "inherit") continue;
    if (d.modes[name] === "none") {
      out[name] = {};
      continue;
    }
    let value: unknown;
    try {
      value = parse(d.affinity[name]);
    } catch (e) {
      throw new Error(`${placementLabels[name]}: ${e instanceof Error ? e.message.split("\n")[0].replace(/:$/, "") : "invalid YAML"}`);
    }
    if (value === null || typeof value !== "object" || Array.isArray(value)) {
      throw new Error(`${placementLabels[name]}: write the rule in YAML, or choose another option`);
    }
    out[name] = value as Record<string, unknown>;
  }
  return out;
}

function cleanToleration(t: Toleration): Toleration {
  const out: Toleration = { ...t, key: t.key?.trim() || undefined, value: t.value?.trim() || undefined, effect: t.effect || undefined };
  if (out.operator === "Exists") delete out.value;
  return out;
}

/** "zone=east", "dedicated=iac:NoSchedule", "Preferred": a field on one line. */
export function placementSummary(name: PlacementFieldName, value: Placement[PlacementFieldName]): string {
  if (isEmpty(value)) return "None";
  if (name === "node_selector") return Object.entries(value as Record<string, string>).map(([k, v]) => `${k}=${v}`).join(", ");
  if (name === "tolerations") {
    return (value as Toleration[])
      .map((t) => `${t.key ?? "any key"}${t.operator === "Exists" ? "" : `=${t.value ?? ""}`}${t.effect ? `:${t.effect}` : ""}`)
      .join(", ");
  }
  const rule = value as Record<string, unknown>;
  const kinds = [rule.requiredDuringSchedulingIgnoredDuringExecution && "Required", rule.preferredDuringSchedulingIgnoredDuringExecution && "Preferred"].filter(Boolean);
  return kinds.length ? (kinds as string[]).join(" and ") : "None";
}

const isMono = (name: PlacementFieldName, value: unknown) => !isEmpty(value) && (name === "node_selector" || name === "tolerations");

/** A value that can't be edited here: inherited, from the installation, or recorded on a run. */
function ReadOnlyValue({ name, value }: { name: PlacementFieldName; value: Placement[PlacementFieldName] }) {
  if (isEmpty(value)) return <span className="muted small">None</span>;
  if (name === "node_selector" || name === "tolerations") return <span className="mono small">{placementSummary(name, value)}</span>;
  return <pre className="placement-yaml">{stringify(value)}</pre>;
}

export interface InheritedPlacementField {
  value: Placement[PlacementFieldName];
  /** "from Acme", "from the installation". */
  from: string;
}

/** The five fields, each with its own choice. `inheritLabel` names the first one. */
export function PlacementEditor({
  draft,
  onChange,
  inherited,
  inheritLabel = "Inherit",
  notSetHelp,
}: {
  draft: PlacementDraft;
  onChange: (d: PlacementDraft) => void;
  /** What applies in this field when this level doesn't set it. */
  inherited: (name: PlacementFieldName) => InheritedPlacementField | undefined;
  inheritLabel?: string;
  notSetHelp: string;
}) {
  const setMode = (name: PlacementFieldName, mode: Mode) => {
    const next: PlacementDraft = { ...draft, modes: { ...draft.modes, [name]: mode } };
    if (mode === "set" && name === "node_selector" && draft.labels.length === 0) next.labels = [{ key: "", value: "" }];
    if (mode === "set" && name === "tolerations" && draft.tolerations.length === 0) next.tolerations = [{ key: "", operator: "Equal", value: "", effect: "NoSchedule" }];
    if (mode === "set" && name !== "node_selector" && name !== "tolerations" && !draft.affinity[name]) {
      next.affinity = { ...draft.affinity, [name]: affinityExample[name] };
    }
    onChange(next);
  };
  const labels = (rows: PlacementDraft["labels"]) => onChange({ ...draft, labels: rows });
  const tolerations = (rows: Toleration[]) => onChange({ ...draft, tolerations: rows });

  return (
    <div className="placement-fields">
      {placementFieldNames.map((name) => {
        const mode = draft.modes[name];
        const from = inherited(name);
        const now = mode === "set" ? "Now: set here." : mode === "none" ? "Now: none, set here. Overrides what comes from above." : from ? `Now: ${from.from}.` : `Now: not set, ${notSetHelp}.`;
        return (
          <div className="setting-block placement-field" key={name}>
            <div className="setting">
              <div>
                <strong>{placementLabels[name]}</strong>
                <p className="muted">
                  {placementHelp[name]} {now}
                </p>
              </div>
              <div role="radiogroup" aria-label={placementLabels[name]} className="segmented">
                {(["inherit", "set", "none"] as Mode[]).map((m) => (
                  <button key={m} type="button" role="radio" aria-checked={mode === m} onClick={() => setMode(name, m)}>
                    {m === "inherit" ? inheritLabel : m === "set" ? "Set" : "None"}
                  </button>
                ))}
              </div>
            </div>
            {mode === "inherit" && from && !isEmpty(from.value) && <ReadOnlyValue name={name} value={from.value} />}
            {mode === "set" && name === "node_selector" && <LabelRows rows={draft.labels} onChange={labels} />}
            {mode === "set" && name === "tolerations" && <TolerationRows rows={draft.tolerations} onChange={tolerations} />}
            {mode === "set" && name !== "node_selector" && name !== "tolerations" && (
              <>
                <textarea className="mono placement-yaml-input" aria-label={`${placementLabels[name]} in YAML`} spellCheck={false} rows={8} value={draft.affinity[name]}
                  onChange={(e) => onChange({ ...draft, affinity: { ...draft.affinity, [name]: e.target.value } })} />
                <p className="hint">
                  Written as in Kubernetes. Runs also carry <span className="mono">firstlume.io/stack-id</span>, so a rule can name one stack.
                </p>
                {name !== "node_affinity" && /requiredDuringScheduling/.test(draft.affinity[name]) && (
                  <p className="notice notice-attention">
                    <strong>Required rules can fail a run when nodes are busy.</strong> If no node fits for the time set at install (5 minutes by default), the run fails, even if one would
                    free up later. <span className="mono">preferred</span> only spreads and never blocks.
                  </p>
                )}
              </>
            )}
            {mode === "set" && name === "tolerations" && draft.tolerations.some(reachesControlPlane) && (
              <p className="notice notice-attention">
                <strong>This lets runs enter control plane nodes.</strong> A run executes the repository’s code. Keep it on nodes meant for that.
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}

const reachesControlPlane = (t: Toleration) => (!t.key?.trim() && t.operator === "Exists") || /control-plane|\/master/.test(t.key ?? "");

function LabelRows({ rows, onChange }: { rows: PlacementDraft["labels"]; onChange: (r: PlacementDraft["labels"]) => void }) {
  return (
    <div className="placement-rows">
      {rows.map((r, i) => (
        <div className="placement-row labels" key={i}>
          <label className="field compact">
            <span className="small muted">Label</span>
            <input className="mono" value={r.key} placeholder="topology.kubernetes.io/zone" onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
          </label>
          <label className="field compact">
            <span className="small muted">Value</span>
            <input className="mono" value={r.value} placeholder="east" onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
          </label>
          <button type="button" className="small-btn" aria-label="Remove label" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            ×
          </button>
        </div>
      ))}
      <button type="button" className="small-btn fit" onClick={() => onChange([...rows, { key: "", value: "" }])}>
        Add label
      </button>
    </div>
  );
}

function TolerationRows({ rows, onChange }: { rows: Toleration[]; onChange: (r: Toleration[]) => void }) {
  const patch = (i: number, p: Partial<Toleration>) => onChange(rows.map((x, j) => (j === i ? { ...x, ...p } : x)));
  return (
    <div className="placement-rows">
      {rows.map((t, i) => (
        <div className="placement-row tolerations" key={i}>
          <label className="field compact">
            <span className="small muted">Key</span>
            <input className="mono" value={t.key ?? ""} placeholder="dedicated" onChange={(e) => patch(i, { key: e.target.value })} />
          </label>
          <label className="field compact">
            <span className="small muted">Operator</span>
            <select value={t.operator ?? "Equal"} onChange={(e) => patch(i, { operator: e.target.value })}>
              <option>Equal</option>
              <option>Exists</option>
            </select>
          </label>
          <label className="field compact">
            <span className="small muted">Value</span>
            <input className="mono" value={t.operator === "Exists" ? "" : t.value ?? ""} placeholder="iac" disabled={t.operator === "Exists"} onChange={(e) => patch(i, { value: e.target.value })} />
          </label>
          <label className="field compact">
            <span className="small muted">Effect</span>
            <select value={t.effect ?? ""} onChange={(e) => patch(i, { effect: e.target.value })}>
              <option value="NoSchedule">NoSchedule</option>
              <option value="PreferNoSchedule">PreferNoSchedule</option>
              <option value="NoExecute">NoExecute</option>
              <option value="">Any</option>
            </select>
          </label>
          <button type="button" className="small-btn" aria-label="Remove toleration" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            ×
          </button>
        </div>
      ))}
      <button type="button" className="small-btn fit" onClick={() => onChange([...rows, { key: "", operator: "Equal", value: "", effect: "NoSchedule" }])}>
        Add toleration
      </button>
    </div>
  );
}

// An agent polls every few seconds; one not seen for this long is probably down.
export const onlineWindowMs = 60_000;
export const agentOnline = (a: Agent) => !!a.last_seen_at && Date.now() - new Date(a.last_seen_at).getTime() < onlineWindowMs;

/** Has a placement set in First Lume, and no permission to use it: it takes no run (R5.6). */
export const agentStopped = (a: Agent) => a.accepts_placement === false && !placementIsZero(a.placement);

/** The fields a run gets from an agent: what is set on it, over the installation. */
function agentFields(a: Agent | undefined) {
  const out: { name: PlacementFieldName; value: Placement[PlacementFieldName]; from: string }[] = [];
  for (const name of placementFieldNames) {
    const own = a?.placement?.[name];
    const install = a?.install_placement?.[name];
    if (own !== undefined && own !== null) out.push({ name, value: own, from: "this agent" });
    else if (install !== undefined && install !== null) out.push({ name, value: install, from: "the installation" });
  }
  return out;
}

/** What an agent's runs use unless a stack, folder or the organization says otherwise. */
export function AgentPlacementSummary({ agent }: { agent: Agent }) {
  const fields = agentFields(agent);
  if (fields.length === 0) {
    return <span className="muted">{agent.install_placement ? "Any node" : "Not known yet"}</span>;
  }
  const sources = [...new Set(fields.map((f) => (f.from === "this agent" ? "set on this agent" : "from the installation")))].join(", ");
  return (
    <>
      {fields.map((f, i) => (
        <span key={f.name}>
          {i > 0 && " · "}
          {isMono(f.name, f.value) ? <span className="mono small">{placementSummary(f.name, f.value)}</span> : `${placementLabels[f.name]}: ${placementSummary(f.name, f.value)}`}
        </span>
      ))}
      <div className="muted small">{sources}</div>
    </>
  );
}

const placementOrigin = (origin: Level) => (origin.kind === "agent" ? "this agent" : origin.kind === "installation" ? "the installation" : levelName(origin));

/** Where a run executes and where each value came from (canvas board 65). */
export function PlacementCard({ run, agents }: { run: Run; agents?: Agent[] }) {
  const agent = agents?.find((a) => a.id === run.agent_id);
  const used = run.placement_used ?? run.placement;
  const rows = placementFieldNames.flatMap((name) => {
    const f = used?.[name];
    return f ? [{ name, value: f.value, from: placementOrigin(f.origin) }] : [];
  });
  if (rows.length === 0 && !agent) return null;
  return (
    <section className="card side-card" aria-label="Where it runs">
      <h2 className="card-label">Where it runs</h2>
      {agent && (
        <p className="small">
          Agent <strong>{agent.name}</strong>
        </p>
      )}
      {rows.length === 0 ? (
        <p className="muted small">Any node.</p>
      ) : (
        <dl className="placement-facts">
          {rows.map((r) => (
            <div key={r.name}>
              <dt className="muted small">{placementLabels[r.name]}</dt>
              <dd className={isMono(r.name, r.value) ? "mono small" : "small"}>{placementSummary(r.name, r.value)}</dd>
              <dd className="muted small">from {r.from}</dd>
            </div>
          ))}
        </dl>
      )}
      {run.placement && <p className="muted small">The organization, folder and stack values were recorded when this run was created.</p>}
    </section>
  );
}

export const noAgentAccepts = (agents?: Agent[]) => !!agents && !agents.some((a) => a.accepts_placement);
export const noOnlineAgentAccepts = (agents?: Agent[]) => !!agents && !agents.some((a) => a.accepts_placement && agentOnline(a));
