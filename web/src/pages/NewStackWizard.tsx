import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import type { DiscoverResponse, GitConnection, GitRepo } from "../api";
import { SearchIcon } from "../icons";
import { useOrg } from "../org";
import { ErrorBanner, useAction, useData } from "../ui";
import { ImportPlan, useImportPlan } from "./ImportState";

/**
 * New stack from the organization's Git connection: repository → branch →
 * one of the .firstlume folders found → name, and the states to import when
 * the infrastructure already exists (spec 0001, 4.5 and 6; spec 0002 R9.7).
 */
export function NewStackWizard({ folderID, conn, onClose, onManual }: {
  folderID: string;
  conn: GitConnection;
  onClose: () => void;
  onManual: () => void;
}) {
  const { api, reloadTree, folderLabel, link } = useOrg();
  const navigate = useNavigate();
  const [repo, setRepo] = useState<GitRepo | null>(null);
  const [branch, setBranch] = useState("");
  const [path, setPath] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [exists, setExists] = useState(false);
  const [importing, setImporting] = useState(false);
  const plan = useImportPlan((files) => api.identifyForNewStack(files, repo?.full_name ?? "", branch, path ?? "."));
  const create = useAction();
  const step = !repo ? 1 : !branch ? 2 : path === null ? 3 : importing ? 5 : 4;

  async function submit() {
    if (!repo || path === null) return;
    await create.run(async () => {
      const st = await api.createStack({ folder_id: folderID, name, git_repository: repo.full_name, repository: "", ref: branch, path, git_token: "" });
      if (importing) {
        try {
          await api.importState(st.id, plan.files, plan.placements, [], false);
        } catch (e) {
          // Nothing was imported: don't leave an empty stack behind (R9.7).
          await api.deleteStack(st.id).catch(() => undefined);
          throw e;
        }
      }
      await reloadTree();
      navigate(link(`s/${st.id}`), importing ? { state: { imported: plan.summary } } : undefined);
      onClose();
    });
  }

  const steps = [
    { n: 1, label: "Repository", value: repo?.full_name },
    { n: 2, label: "Branch", value: step > 2 ? branch : undefined },
    { n: 3, label: "Infrastructure", value: step > 3 ? path ?? undefined : undefined },
    { n: 4, label: "Name", value: undefined },
    ...(exists ? [{ n: 5, label: "Import", value: undefined }] : []),
  ];

  return (
    <div className="wizard">
      <p className="muted">
        In <strong className="ink">{folderLabel(folderID)}</strong>. It will inherit the settings and secrets above it.
      </p>
      <ol className="wizard-steps">
        {steps.map((s) => (
          <li key={s.n} className={s.n < step ? "done" : s.n === step ? "current" : ""}>
            <span className="wizard-num">{s.n < step ? "✓" : s.n}</span>
            {s.label}
            {s.value && <span className="mono muted">{s.value}</span>}
          </li>
        ))}
      </ol>

      {step === 1 && <PickRepo conn={conn} onPick={(r) => (setRepo(r), setBranch(r.default_branch), setPath(null))} />}
      {step >= 2 && step < 4 && repo && (
        <PickStack
          repo={repo}
          branch={branch}
          onBranch={(b) => (setBranch(b), setPath(null))}
          onPick={(p) => {
            setPath(p);
            setName(p === "." ? repo.name : p.split("/").pop() ?? p);
          }}
        />
      )}
      {step === 4 && repo && (
        <form
          className="wizard-body"
          onSubmit={(e) => {
            e.preventDefault();
            if (exists) setImporting(true);
            else void submit();
          }}
        >
          <div className="notice">
            <span className="mono">{repo.full_name}</span> · <span className="mono">{path}</span> @ <span className="mono">{branch}</span>
          </div>
          <label className="field">
            Stack name
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus required />
            <span className="hint">Suggested from the folder. Must be unique in {folderLabel(folderID)}.</span>
          </label>
          <fieldset className="start-from">
            <legend>Does this infrastructure already exist?</legend>
            <label className={`found${exists ? "" : " selected"}`}>
              <input type="radio" name="start-from" checked={!exists} onChange={() => setExists(false)} />
              <span className="grow">
                <span className="start-title">No, start empty</span>
                <span className="muted small">The first apply creates everything.</span>
              </span>
            </label>
            <label className={`found${exists ? " selected" : ""}`}>
              <input type="radio" name="start-from" checked={exists} onChange={() => setExists(true)} />
              <span className="grow">
                <span className="start-title">Yes, and it has a Terraform state</span>
                <span className="muted small">
                  Another pipeline or someone’s machine manages it today. Upload its <span className="mono">.tfstate</span> files, or a <span className="mono">tar.gz</span>,
                  in any folder layout. Nothing is recreated.
                </span>
              </span>
            </label>
          </fieldset>
          {create.error && <div className="form-error">{create.error}</div>}
          <div className="form-actions">
            <button type="button" onClick={() => setPath(null)}>
              Back
            </button>
            <button className="primary" disabled={create.busy || !name.trim()}>
              {exists ? "Continue to import" : create.busy ? "Creating…" : "Create stack"}
            </button>
          </div>
        </form>
      )}
      {step === 5 && repo && (
        <div className="wizard-body">
          <p className="muted small">
            <strong className="ink">{name}</strong> in {folderLabel(folderID)}, from <span className="mono">{repo.full_name}</span> · <span className="mono">{path}</span> @{" "}
            <span className="mono">{branch}</span>
          </p>
          <ImportPlan plan={plan} rootLabel={path ?? "."} onPickAnotherFolder={() => (setImporting(false), setPath(null))} />
          {create.error && <div className="form-error">{create.error}</div>}
          <div className="form-actions">
            <button type="button" onClick={() => setImporting(false)}>
              Back
            </button>
            <span className="muted small grow-right">{plan.hint}</span>
            <button className="primary" disabled={create.busy || !plan.ready} onClick={() => void submit()}>
              {create.busy ? "Creating…" : plan.ready ? `Create stack and import ${plan.summary.states} state${plan.summary.states === 1 ? "" : "s"}` : "Create stack and import"}
            </button>
          </div>
        </div>
      )}
      {step < 4 && (
        <div className="wizard-footer">
          {step > 1 ? (
            <button type="button" onClick={() => setRepo(null)}>
              Back
            </button>
          ) : (
            <span />
          )}
          <button type="button" className="link" onClick={onManual}>
            Use a repository URL instead
          </button>
        </div>
      )}
    </div>
  );
}

function PickRepo({ conn, onPick }: { conn: GitConnection; onPick: (r: GitRepo) => void }) {
  const { api } = useOrg();
  const repos = useData(() => api.gitRepos(), [api]);
  const [q, setQ] = useState("");
  if (repos.error) return <ErrorBanner error={repos.error} />;
  const list = (repos.data ?? []).filter((r) => !q || r.full_name.toLowerCase().includes(q.toLowerCase()));
  return (
    <div className="wizard-body">
      <label className="search">
        <SearchIcon />
        <input type="search" placeholder={`Find a repository in ${conn.account}`} value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
      </label>
      <div className="pick-list" role="listbox" aria-label="Repositories">
        {!repos.data && <p className="muted small">Loading repositories from {conn.url}…</p>}
        {repos.data && list.length === 0 && <p className="muted small">No repository matches.</p>}
        {list.map((r) => (
          <button key={r.full_name} role="option" aria-selected={false} className="pick-item" disabled={r.empty} onClick={() => onPick(r)}>
            <span className="mono">{r.full_name}</span>
            <span className="muted small">{r.empty ? "empty" : r.default_branch}</span>
            {r.private && <span className="badge idle">private</span>}
          </button>
        ))}
      </div>
    </div>
  );
}

function PickStack({ repo, branch, onBranch, onPick }: { repo: GitRepo; branch: string; onBranch: (b: string) => void; onPick: (path: string) => void }) {
  const { api } = useOrg();
  const branches = useData(() => api.gitBranches(repo.full_name), [api, repo.full_name]);
  const found = useData<DiscoverResponse>(() => api.discover(repo.full_name, branch), [api, repo.full_name, branch]);
  const [selected, setSelected] = useState<string | null>(null);
  useEffect(() => setSelected(null), [branch]);
  const usable = found.data?.stacks.filter((s) => !s.error && !s.used_by) ?? [];

  return (
    <div className="wizard-body">
      <label className="field inline-field">
        Branch
        <select value={branch} onChange={(e) => onBranch(e.target.value)}>
          {(branches.data ?? [branch]).map((b) => (
            <option key={b} value={b}>
              {b}
            </option>
          ))}
        </select>
      </label>
      <div className="section-head">
        <h3 className="wizard-title">Choose the infrastructure to manage</h3>
        {found.data && (
          <span className="muted small">
            Found {found.data.stacks.length} <span className="mono">.firstlume</span> folder{found.data.stacks.length === 1 ? "" : "s"} at{" "}
            <span className="mono">{found.data.commit.slice(0, 7)}</span>
          </span>
        )}
      </div>
      {found.error && <ErrorBanner error={found.error} />}
      {!found.data && !found.error && <p className="muted small">Looking for .firstlume folders on {branch}…</p>}
      {found.data && found.data.stacks.length === 0 && (
        <div className="notice">
          No <span className="mono">.firstlume</span> folders on {branch}. Add one next to the Terraform code you want to manage:
          <pre className="code-block">{"environments/prod/.firstlume/config.yaml\nversion: 1\nengine: terraform\nengine_version: 1.15.6"}</pre>
        </div>
      )}
      <div role="radiogroup" aria-label=".firstlume folders" className="found-list">
        {found.data?.stacks.map((s) => {
          const disabled = !!s.error || !!s.used_by;
          return (
            <label key={s.path} className={`found${selected === s.path ? " selected" : ""}${disabled ? " disabled" : ""}`}>
              <input type="radio" name="found" disabled={disabled} checked={selected === s.path} onChange={() => setSelected(s.path)} />
              <span className="grow">
                <span className="mono found-path">{s.path}</span>
                <span className="muted small">
                  {s.error
                    ? s.error
                    : s.used_by
                      ? `Already managed by stack “${s.used_by.name}”`
                      : `${s.config?.engine} ${s.config?.engine_version}${s.config?.terragrunt_version ? ` · terragrunt ${s.config.terragrunt_version}${s.config.run_all ? " (run --all)" : ""}` : ""}`}
                </span>
              </span>
              <span className={`badge ${s.error ? "bad" : s.used_by ? "idle" : "ok"}`}>{s.error ? "Invalid config" : s.used_by ? "In use" : "Available"}</span>
            </label>
          );
        })}
      </div>
      <div className="form-actions">
        <button className="primary" disabled={!selected} onClick={() => selected !== null && onPick(selected)}>
          Continue
        </button>
      </div>
      {found.data && usable.length === 0 && found.data.stacks.length > 0 && (
        <p className="hint">Every .firstlume folder here is in use or invalid. Fix the config in Git, or pick another branch.</p>
      )}
    </div>
  );
}
