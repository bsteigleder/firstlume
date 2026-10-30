import { useState, type FormEvent } from "react";
import { api, type CreateTemplateRequest } from "../api";
import { Empty, ErrorBanner, PageHeader, repoLabel, timeAgo, useAction, useData } from "../ui";

export function TemplatesPage() {
  const [creating, setCreating] = useState(false);
  const { data, error, reload } = useData(api.listTemplates, []);
  const del = useAction();

  async function remove(id: string, name: string) {
    if (!confirm(`Delete template "${name}"?`)) return;
    if (await del.run(() => api.deleteTemplate(id))) await reload();
  }

  return (
    <>
      <PageHeader
        title="Templates"
        subtitle="A template points at Terraform or OpenTofu code in a Git repository."
        actions={
          !creating && (
            <button className="primary" onClick={() => setCreating(true)}>
              New template
            </button>
          )
        }
      />
      <ErrorBanner error={error ?? del.error} onDismiss={del.error ? () => del.setError(null) : undefined} />
      {creating && (
        <NewTemplate
          onClose={() => setCreating(false)}
          onCreated={async () => {
            setCreating(false);
            await reload();
          }}
        />
      )}
      {data &&
        (data.length === 0 ? (
          <Empty>No templates yet.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Source</th>
                <th>Engine</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((t) => (
                <tr key={t.id}>
                  <td>
                    <strong>{t.name}</strong>
                    {t.has_git_token && <span className="tag">private repo</span>}
                  </td>
                  <td className="mono muted source">
                    {repoLabel(t.repository)}
                    {t.path && `/${t.path}`} @ {t.default_ref}
                  </td>
                  <td>
                    {t.engine} {t.engine_version}
                    {t.terragrunt_version && <span className="muted"> + terragrunt {t.terragrunt_version}</span>}
                  </td>
                  <td className="muted">{timeAgo(t.created_at)}</td>
                  <td className="right">
                    <button className="link" onClick={() => remove(t.id, t.name)} disabled={del.busy}>
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
    </>
  );
}

const blank: CreateTemplateRequest = {
  name: "",
  repository: "",
  path: "",
  default_ref: "main",
  engine: "terraform",
  engine_version: "1.15.6",
  terragrunt_version: "",
  git_token: "",
};

function NewTemplate({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState(blank);
  const { busy, error, run } = useAction();
  const set = (k: keyof CreateTemplateRequest) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (await run(() => api.createTemplate(form))) onCreated();
  }

  return (
    <form className="card form-card" onSubmit={submit}>
      <h2>New template</h2>
      <div className="grid-2">
        <label>
          Name
          <input value={form.name} onChange={set("name")} placeholder="network" autoFocus required />
        </label>
        <label>
          Repository
          <input value={form.repository} onChange={set("repository")} placeholder="https://github.com/acme/infra.git" required />
        </label>
        <label>
          Path <span className="optional">optional</span>
          <input value={form.path} onChange={set("path")} placeholder="modules/network" />
        </label>
        <label>
          Default ref
          <input value={form.default_ref} onChange={set("default_ref")} />
        </label>
        <label>
          Engine
          <select value={form.engine} onChange={set("engine")}>
            <option value="terraform">Terraform</option>
            <option value="opentofu">OpenTofu</option>
          </select>
        </label>
        <label>
          Engine version
          <input value={form.engine_version} onChange={set("engine_version")} placeholder="1.15.6" required />
        </label>
        <label>
          Terragrunt version <span className="optional">optional</span>
          <input value={form.terragrunt_version} onChange={set("terragrunt_version")} placeholder="0.77.0" />
        </label>
        <label>
          Git token <span className="optional">private repositories</span>
          <input type="password" value={form.git_token} onChange={set("git_token")} autoComplete="off" />
        </label>
      </div>
      {error && <div className="form-error">{error}</div>}
      <div className="form-actions">
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" disabled={busy}>
          {busy ? "Creating…" : "Create template"}
        </button>
      </div>
    </form>
  );
}
