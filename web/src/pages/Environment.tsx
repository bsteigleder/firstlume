import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api, isTerminal, type Environment, type RunType, type Variable } from "../api";
import {
  driftIntervals,
  duration,
  Empty,
  ErrorBanner,
  PageHeader,
  PlanCounts,
  repoLabel,
  shortID,
  StatusBadge,
  timeAgo,
  useAction,
  useData,
} from "../ui";

export function EnvironmentPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const env = useData(async () => {
    const e = await api.getEnvironment(id);
    const templates = await api.listTemplates();
    return { env: e, template: templates.find((t) => t.id === e.template_id) };
  }, [id], { poll: true, pollMs: 10000 }); // picks up drift results
  const runs = useData(() => api.listRuns(id), [id], { poll: true, pollMs: 3000 });
  const action = useAction();

  const active = runs.data?.find((r) => !isTerminal(r.status));

  async function start(type: RunType) {
    if (type === "destroy" && !confirm(`Destroy every resource managed by "${env.data?.env.name}"? You will still review the plan first.`)) {
      return;
    }
    await action.run(async () => {
      const run = await api.createRun(id, type);
      navigate(`/runs/${run.id}`);
    });
  }

  async function remove() {
    if (!confirm(`Delete environment "${env.data?.env.name}"? This does not destroy its infrastructure.`)) return;
    await action.run(async () => {
      await api.deleteEnvironment(id);
      navigate("/environments");
    });
  }

  if (env.error && !env.data) return <ErrorBanner error={env.error} />;
  if (!env.data) return null;
  const { env: e, template } = env.data;

  return (
    <>
      <div className="breadcrumb">
        <Link to="/environments">Environments</Link> /
      </div>
      <PageHeader
        title={e.name}
        subtitle={
          <>
            {template ? (
              <>
                <span className="mono">
                  {repoLabel(template.repository)}
                  {template.path && `/${template.path}`}
                </span>
                <span className="sep">·</span>
                <span className="mono">{e.ref || template.default_ref}</span>
                <span className="sep">·</span>
                {template.engine} {template.engine_version}
              </>
            ) : (
              "template not found"
            )}
            {e.auto_approve && <span className="tag">auto-apply</span>}
          </>
        }
        actions={
          <>
            <button onClick={() => start("drift")} disabled={action.busy} title="Plan without applying, to see if the infrastructure still matches the code">
              Check drift
            </button>
            <button onClick={() => start("plan")} disabled={action.busy}>
              Plan
            </button>
            <button className="primary" onClick={() => start("apply")} disabled={action.busy}>
              Plan &amp; apply
            </button>
            <button className="danger-outline" onClick={() => start("destroy")} disabled={action.busy}>
              Destroy
            </button>
          </>
        }
      />
      <ErrorBanner error={action.error} onDismiss={() => action.setError(null)} />
      {active && (
        <div className="notice">
          A run is in progress: <Link to={`/runs/${active.id}`}>{shortID(active.id)}</Link> <StatusBadge status={active.status} />. New runs
          wait for it to finish.
        </div>
      )}
      {e.drift?.status === "drifted" && (
        <div className="notice notice-attention">
          <strong>Drift detected</strong> {timeAgo(e.drift.checked_at)}: the infrastructure no longer matches the code.
          {e.drift.run_id && <Link to={`/runs/${e.drift.run_id}`}>See what changed</Link>}
        </div>
      )}
      {e.drift?.status === "error" && (
        <div className="notice notice-danger">
          The last drift check failed {timeAgo(e.drift.checked_at)}.
          {e.drift.run_id && <Link to={`/runs/${e.drift.run_id}`}>See the logs</Link>}
        </div>
      )}

      <section>
        <h2>Runs</h2>
        <ErrorBanner error={runs.error} />
        {runs.data &&
          (runs.data.length === 0 ? (
            <Empty>No runs yet. Start with a plan.</Empty>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>Run</th>
                  <th>Type</th>
                  <th>Status</th>
                  <th>Changes</th>
                  <th>Commit</th>
                  <th>Duration</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {runs.data.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <Link className="row-link mono" to={`/runs/${r.id}`}>
                        {shortID(r.id)}
                      </Link>
                    </td>
                    <td>{r.type}</td>
                    <td>
                      <StatusBadge status={r.status} />
                    </td>
                    <td>
                      <PlanCounts summary={r.plan_summary} />
                    </td>
                    <td className="mono muted">{r.commit_sha?.slice(0, 7) ?? "—"}</td>
                    <td className="muted">{duration(r.started_at, r.finished_at)}</td>
                    <td className="muted">{timeAgo(r.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ))}
      </section>

      <Settings env={e} onSaved={env.reload} />

      <Variables envID={id} />

      <section>
        <h2>Danger zone</h2>
        <div className="card danger-card">
          <div>
            <strong>Delete environment</strong>
            <p className="muted">Removes the environment and its run history. Run a destroy first to remove its infrastructure.</p>
          </div>
          <button className="danger" onClick={remove} disabled={action.busy || !!active}>
            Delete
          </button>
        </div>
      </section>
    </>
  );
}

function Settings({ env, onSaved }: { env: Environment; onSaved: () => Promise<void> }) {
  const [requireApproval, setRequireApproval] = useState(!env.auto_approve);
  const [driftMinutes, setDriftMinutes] = useState(env.drift_interval_minutes);
  const save = useAction();

  // Follow the server when nothing is being edited (e.g. changed elsewhere).
  const dirty = requireApproval !== !env.auto_approve || driftMinutes !== env.drift_interval_minutes;
  useEffect(() => {
    if (!save.busy && !dirty) {
      setRequireApproval(!env.auto_approve);
      setDriftMinutes(env.drift_interval_minutes);
    }
  }, [env.auto_approve, env.drift_interval_minutes]);

  async function submit() {
    if (await save.run(() => api.updateEnvironment(env.id, { auto_approve: !requireApproval, drift_interval_minutes: driftMinutes }))) {
      await onSaved();
    }
  }

  const intervals = driftIntervals.some((d) => d.minutes === driftMinutes)
    ? driftIntervals
    : [...driftIntervals, { minutes: driftMinutes, label: `Every ${driftMinutes} minutes` }];

  return (
    <section>
      <h2>Settings</h2>
      <div className="card settings">
        <div className="setting">
          <div>
            <strong>Require approval before apply</strong>
            <p className="muted">
              Applies stop after the plan until someone approves them. When off, a successful plan is applied right away. It also applies to runs
              already queued.
            </p>
          </div>
          <label className="switch">
            <input type="checkbox" checked={requireApproval} onChange={(ev) => setRequireApproval(ev.target.checked)} />
            <span className="sr-only">Require approval before apply</span>
          </label>
        </div>
        <div className="setting">
          <div>
            <strong>Drift detection</strong>
            <p className="muted">
              Runs a plan on a schedule and reports when the infrastructure no longer matches the code. It never applies anything. Changes on the
              branch that were not applied yet also show up as drift.
            </p>
          </div>
          <select value={driftMinutes} onChange={(ev) => setDriftMinutes(Number(ev.target.value))} aria-label="Drift detection interval">
            {intervals.map((d) => (
              <option key={d.minutes} value={d.minutes}>
                {d.label}
              </option>
            ))}
          </select>
        </div>
        {save.error && <div className="form-error">{save.error}</div>}
        <div className="form-actions">
          <button className="primary" onClick={submit} disabled={!dirty || save.busy}>
            {save.busy ? "Saving…" : "Save settings"}
          </button>
        </div>
      </div>
    </section>
  );
}

interface EditableVariable extends Variable {
  // Set for sensitive variables already stored on the server, whose value is never sent back.
  stored?: boolean;
}

function Variables({ envID }: { envID: string }) {
  const { data, error, reload } = useData(() => api.listVariables(envID), [envID]);
  const [vars, setVars] = useState<EditableVariable[]>([]);
  const [dirty, setDirty] = useState(false);
  const save = useAction();

  useEffect(() => {
    if (data) {
      setVars(data.map((v) => ({ ...v, stored: v.sensitive })));
      setDirty(false);
    }
  }, [data]);

  function update(i: number, patch: Partial<EditableVariable>) {
    setVars((vs) => vs.map((v, j) => (j === i ? { ...v, ...patch } : v)));
    setDirty(true);
  }

  async function submit() {
    const ok = await save.run(() =>
      api.putVariables(
        envID,
        vars.map(({ key, category, sensitive, value }) => ({ key: key.trim(), category, sensitive, value: value ?? "" })),
      ),
    );
    if (ok) await reload();
  }

  return (
    <section>
      <h2>Variables</h2>
      <ErrorBanner error={error} />
      <div className="card">
        {vars.length === 0 ? (
          <p className="muted">No variables. Terraform variables are passed as TF_VAR_*; environment variables as-is.</p>
        ) : (
          <table className="table vars">
            <thead>
              <tr>
                <th>Key</th>
                <th>Kind</th>
                <th>Value</th>
                <th>Sensitive</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {vars.map((v, i) => (
                <tr key={i}>
                  <td>
                    <input className="mono" value={v.key} onChange={(e) => update(i, { key: e.target.value })} placeholder="name" />
                  </td>
                  <td>
                    <select value={v.category} onChange={(e) => update(i, { category: e.target.value as Variable["category"] })}>
                      <option value="terraform">Terraform</option>
                      <option value="env">Env var</option>
                    </select>
                  </td>
                  <td>
                    <input
                      className="mono"
                      type={v.sensitive ? "password" : "text"}
                      value={v.value ?? ""}
                      onChange={(e) => update(i, { value: e.target.value })}
                      placeholder={v.stored ? "•••••• (unchanged)" : "value"}
                      autoComplete="off"
                    />
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      checked={v.sensitive}
                      onChange={(e) => update(i, { sensitive: e.target.checked, stored: false })}
                      aria-label="Sensitive"
                    />
                  </td>
                  <td>
                    <button className="link" onClick={() => (setVars((vs) => vs.filter((_, j) => j !== i)), setDirty(true))}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {save.error && <div className="form-error">{save.error}</div>}
        <div className="form-actions">
          <button onClick={() => (setVars((vs) => [...vs, { key: "", category: "terraform", sensitive: false, value: "" }]), setDirty(true))}>
            Add variable
          </button>
          <button className="primary" onClick={submit} disabled={!dirty || save.busy}>
            {save.busy ? "Saving…" : "Save variables"}
          </button>
        </div>
      </div>
    </section>
  );
}
