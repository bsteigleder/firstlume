import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { api, isTerminal, type Environment, type Run, type Template } from "../api";
import { DriftBadge, Empty, ErrorBanner, PageHeader, StatusBadge, timeAgo, useAction, useData } from "../ui";

interface Row {
  env: Environment;
  template?: Template;
  lastRun?: Run;
}

async function loadRows(): Promise<{ rows: Row[]; templates: Template[] }> {
  const [envs, templates] = await Promise.all([api.listEnvironments(), api.listTemplates()]);
  const lastRuns = await Promise.all(envs.map((e) => api.listRuns(e.id, 1)));
  return {
    templates,
    rows: envs.map((env, i) => ({
      env,
      template: templates.find((t) => t.id === env.template_id),
      lastRun: lastRuns[i][0],
    })),
  };
}

export function EnvironmentsPage() {
  const [creating, setCreating] = useState(false);
  const { data, error } = useData(loadRows, [], {
    poll: true,
    pollMs: 5000,
  });

  return (
    <>
      <PageHeader
        title="Environments"
        subtitle="Each environment deploys one template with its own variables and run history."
        actions={
          !creating && (
            <button className="primary" onClick={() => setCreating(true)}>
              New environment
            </button>
          )
        }
      />
      <ErrorBanner error={error} />
      {creating && data && <NewEnvironment templates={data.templates} onClose={() => setCreating(false)} />}
      {data &&
        (data.rows.length === 0 ? (
          <Empty>
            No environments yet.{" "}
            {data.templates.length === 0 ? (
              <>
                Start by <Link to="/templates">creating a template</Link>.
              </>
            ) : (
              "Create one to start planning."
            )}
          </Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Template</th>
                <th>Last run</th>
                <th>Drift</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {data.rows.map(({ env, template, lastRun }) => (
                <tr key={env.id}>
                  <td>
                    <Link className="row-link" to={`/environments/${env.id}`}>
                      {env.name}
                    </Link>
                    {env.auto_approve && <span className="tag">auto-apply</span>}
                  </td>
                  <td className="muted">{template?.name ?? "—"}</td>
                  <td>
                    {lastRun ? (
                      <Link to={`/runs/${lastRun.id}`} className="run-cell">
                        <StatusBadge status={lastRun.status} />
                        <span className="muted">{lastRun.type}</span>
                      </Link>
                    ) : (
                      <span className="muted">never run</span>
                    )}
                  </td>
                  <td>
                    {env.drift?.run_id ? (
                      <Link to={`/runs/${env.drift.run_id}`} title={`Checked ${timeAgo(env.drift.checked_at)}`}>
                        <DriftBadge drift={env.drift} intervalMinutes={env.drift_interval_minutes} />
                      </Link>
                    ) : (
                      <DriftBadge drift={env.drift} intervalMinutes={env.drift_interval_minutes} />
                    )}
                  </td>
                  <td className="muted">
                    {timeAgo(lastRun ? (isTerminal(lastRun.status) ? lastRun.finished_at : lastRun.created_at) : env.created_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
    </>
  );
}

function NewEnvironment({ templates, onClose }: { templates: Template[]; onClose: () => void }) {
  const navigate = useNavigate();
  const [templateID, setTemplateID] = useState(templates[0]?.id ?? "");
  const [name, setName] = useState("");
  const [ref, setRef] = useState("");
  const [requireApproval, setRequireApproval] = useState(true);
  const { busy, error, run } = useAction();

  if (templates.length === 0) {
    return (
      <div className="card form-card">
        <p>
          An environment needs a template. <Link to="/templates">Create a template</Link> first.
        </p>
        <div className="form-actions">
          <button onClick={onClose}>Close</button>
        </div>
      </div>
    );
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const env = await api.createEnvironment({ template_id: templateID, name, ref, auto_approve: !requireApproval });
      navigate(`/environments/${env.id}`);
    });
  }

  const selected = templates.find((t) => t.id === templateID);
  return (
    <form className="card form-card" onSubmit={submit}>
      <h2>New environment</h2>
      <div className="grid-2">
        <label>
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="production" autoFocus required />
        </label>
        <label>
          Template
          <select value={templateID} onChange={(e) => setTemplateID(e.target.value)}>
            {templates.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Git ref <span className="optional">optional</span>
          <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder={selected?.default_ref ?? "main"} />
        </label>
        <label className="checkbox">
          <input type="checkbox" checked={requireApproval} onChange={(e) => setRequireApproval(e.target.checked)} />
          Require approval before apply
        </label>
      </div>
      {error && <div className="form-error">{error}</div>}
      <div className="form-actions">
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" disabled={busy || !name}>
          {busy ? "Creating…" : "Create environment"}
        </button>
      </div>
    </form>
  );
}
