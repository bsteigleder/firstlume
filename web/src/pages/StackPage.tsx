import { useEffect, useState, type FormEvent } from "react";
import { Link, NavLink, useNavigate, useParams } from "react-router";
import { settingsPath, type Stack } from "../api";
import { ExternalIcon, PlayIcon } from "../icons";
import { PageTitle, useCrumbs, useOrg } from "../org";
import {
  commitURL,
  duration,
  Empty,
  ErrorBanner,
  Initials,
  PlanCounts,
  repoLabel,
  repoWebURL,
  StatusBadge,
  timeAgo,
  useAction,
  useData,
} from "../ui";
import { RenameStackDialog } from "./dialogs";
import { MoveButton } from "./MoveStack";
import { RunDialog } from "./RunDialog";
import { SettingsEditor } from "./SettingsEditor";
import { StackDeclaration } from "./StackDeclaration";
import { StackResources } from "./StackResources";

export function StackPage({ tab }: { tab: "runs" | "resources" | "settings" | "source" }) {
  const { stackId = "" } = useParams();
  const { api, org, link, folderChain, reloadTree } = useOrg();
  const stack = useData(() => api.getStack(stackId), [api, stackId], { poll: true, pollMs: 10000 });
  const units = useData(() => api.stateUnits(stackId), [api, stackId], { poll: true, pollMs: 15000 });
  const [running, setRunning] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const chain = folderChain(stack.data?.folder_id);

  useCrumbs([
    { label: org.name, to: link() },
    ...chain.map((f) => ({ label: f.name, to: link(`f/${f.id}`) })),
    { label: stack.data?.name ?? "…", to: link(`s/${stackId}`) },
  ]);

  if (stack.error && !stack.data) return <ErrorBanner error={stack.error} />;
  if (!stack.data) return null;
  const s = stack.data;
  const web = repoWebURL(s.repository);
  const resourceCount = units.data?.reduce((n, u) => n + u.resources, 0);

  return (
    <div className="page">
      <PageTitle
        title={s.name}
        sub={
          <span className="source-line">
            {web ? (
              <a href={web} target="_blank" rel="noreferrer" className="mono">
                {repoLabel(s.repository)}
              </a>
            ) : (
              <span className="mono">{repoLabel(s.repository)}</span>
            )}
            <span className="mono">· {s.path}</span>
            <span className="mono">@ {s.ref}</span>
            {s.config && (
              <span className="mono muted">
                · {s.config.engine} {s.config.engine_version}
                {s.config.terragrunt_version && ` + terragrunt ${s.config.terragrunt_version}${s.config.run_all ? " (run --all)" : ""}`}
              </span>
            )}
          </span>
        }
        actions={
          <>
            <button onClick={() => setRenaming(true)}>Rename</button>
            <MoveButton stack={s} />
            <button className="primary" onClick={() => setRunning(true)}>
              <PlayIcon /> Run…
            </button>
          </>
        }
      />

      <div role="tablist" className="tabs">
        <NavLink end to={link(`s/${s.id}`)} role="tab">
          Runs
        </NavLink>
        <NavLink to={link(`s/${s.id}/resources`)} role="tab">
          Resources
          {resourceCount !== undefined && resourceCount > 0 && <span className="badge idle tab-count">{resourceCount}</span>}
        </NavLink>
        <NavLink to={link(`s/${s.id}/settings`)} role="tab">
          Variables &amp; settings
        </NavLink>
        <NavLink to={link(`s/${s.id}/source`)} role="tab">
          Source
        </NavLink>
      </div>

      {tab === "runs" && <Runs stack={s} />}
      {tab === "resources" && <StackResources stack={s} />}
      {tab === "settings" && <SettingsEditor level={{ kind: "stack", id: s.id, name: s.name }} path={settingsPath({ kind: "stack", id: s.id })} />}
      {tab === "source" && (
        <Source
          stack={s}
          onSaved={async () => {
            await stack.reload();
            await reloadTree();
          }}
        />
      )}
      {running && <RunDialog stack={s} onClose={() => setRunning(false)} />}
      {renaming && <RenameStackDialog stack={s} onClose={() => setRenaming(false)} onSaved={stack.reload} />}
    </div>
  );
}

function Runs({ stack }: { stack: Stack }) {
  const { api, link } = useOrg();
  const runs = useData(() => api.listRuns(stack.id), [api, stack.id], { poll: true, pollMs: 4000 });
  if (runs.error && !runs.data) return <ErrorBanner error={runs.error} />;
  if (!runs.data) return null;
  const waiting = runs.data.find((r) => r.status === "awaiting_approval");

  return (
    <>
      {waiting && (
        <Link to={link(`r/${waiting.id}`)} className="banner banner-attention">
          <StatusBadge status="awaiting_approval" />
          <span className="banner-text">
            <strong>Run {waiting.id.slice(0, 8)}</strong> plans <PlanCounts summary={waiting.plan_summary} />
            {waiting.commit_message && <> from {waiting.commit_author_name ? `${waiting.commit_author_name}’s` : "the"} commit “{waiting.commit_message}”</>}.
          </span>
          <span className="banner-cta">Review plan →</span>
        </Link>
      )}
      {stack.drift?.status === "drifted" && (
        <Link to={link(`r/${stack.drift.run_id}`)} className="banner banner-attention">
          <span className="badge warn">Drifted</span>
          <span className="banner-text">The infrastructure no longer matches the code (checked {timeAgo(stack.drift.checked_at)}).</span>
          <span className="banner-cta">See what differs →</span>
        </Link>
      )}

      {runs.data.length === 0 ? (
        <Empty>No runs yet. Use Run… to plan this stack.</Empty>
      ) : (
        <div className="table-box">
          <table className="table">
            <thead>
              <tr>
                <th>Status</th>
                <th>Commit</th>
                <th>Changes</th>
                <th>Started by</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {runs.data.map((r) => {
                const commit = commitURL(stack.repository, r.commit_sha);
                return (
                  <tr key={r.id}>
                    <td>
                      <Link to={link(`r/${r.id}`)} className="plain">
                        <StatusBadge status={r.status} />
                      </Link>
                      <div className="muted small">{r.type === "drift" ? "drift check" : r.type}</div>
                    </td>
                    <td>
                      <Link to={link(`r/${r.id}`)} className="row-title">
                        {r.commit_message || `${r.type} on ${r.ref}`}
                      </Link>
                      <div className="commit-meta">
                        {r.commit_author_name && (
                          <>
                            <Initials name={r.commit_author_name} size={18} /> <span>{r.commit_author_name}</span>
                          </>
                        )}
                        {r.commit_sha &&
                          (commit ? (
                            <a className="mono" href={commit} target="_blank" rel="noreferrer" aria-label={`Open commit ${r.commit_sha.slice(0, 7)} in Git`}>
                              {r.commit_sha.slice(0, 7)} <ExternalIcon size={11} />
                            </a>
                          ) : (
                            <span className="mono">{r.commit_sha.slice(0, 7)}</span>
                          ))}
                      </div>
                    </td>
                    <td>
                      <PlanCounts summary={r.plan_summary} />
                    </td>
                    <td className="small">
                      {r.created_by === "drift-detection" ? "schedule" : r.created_by}
                      {r.approved_by && <div className="muted">approved by {r.approved_by}</div>}
                    </td>
                    <td className="muted small">
                      {timeAgo(r.created_at)}
                      <div>{duration(r.started_at, r.finished_at)}</div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function Source({ stack, onSaved }: { stack: Stack; onSaved: () => Promise<void> }) {
  const { api, link } = useOrg();
  const navigate = useNavigate();
  const [form, setForm] = useState({ repository: stack.repository, ref: stack.ref, path: stack.path, git_token: "" });
  const [changeToken, setChangeToken] = useState(false);
  const save = useAction();
  const del = useAction();
  useEffect(() => setForm({ repository: stack.repository, ref: stack.ref, path: stack.path, git_token: "" }), [stack.repository, stack.ref, stack.path]);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const dirty = form.repository !== stack.repository || form.ref !== stack.ref || form.path !== stack.path || changeToken;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const ok = await save.run(() =>
      api.updateStack(stack.id, { repository: form.repository, ref: form.ref, path: form.path, ...(changeToken ? { git_token: form.git_token } : {}) }),
    );
    if (ok) {
      setChangeToken(false);
      await onSaved();
    }
  }

  return (
    <div className="stack-source">
      <form className="card form-card" onSubmit={submit}>
        <h2>Where the code comes from</h2>
        <div className="grid-2">
          <label className="field span-2">
            Repository URL
            <input className="mono" value={form.repository} onChange={set("repository")} required disabled={!!stack.connection_id} />
            {stack.connection_id && <span className="hint">From the organization’s Git connection: it clones with the connection’s service token.</span>}
          </label>
          <label className="field">
            Branch
            <input className="mono" value={form.ref} onChange={set("ref")} required />
          </label>
          <label className="field">
            Folder that contains <span className="mono">.firstlume</span>
            <input className="mono" value={form.path} onChange={set("path")} />
          </label>
          {!stack.connection_id && <label className="field span-2">
            Access token{" "}
            <span className="optional">{stack.has_git_token ? "saved, never shown" : "none — only needed for private repositories"}</span>
            {changeToken ? (
              <input type="password" autoComplete="off" value={form.git_token} onChange={set("git_token")} placeholder="New token (empty removes it)" />
            ) : (
              <button type="button" className="small-btn fit" onClick={() => setChangeToken(true)}>
                {stack.has_git_token ? "Replace or remove token" : "Add a token"}
              </button>
            )}
          </label>}
        </div>
        {(form.path !== stack.path || form.repository !== stack.repository) && (
          <p className="notice notice-attention">Changing the repository or folder changes which infrastructure this stack manages.</p>
        )}
        {save.error && <div className="form-error">{save.error}</div>}
        <div className="form-actions">
          <button className="primary" disabled={!dirty || save.busy}>
            {save.busy ? "Saving…" : "Save source"}
          </button>
        </div>
      </form>

      <section className="card">
        <div className="section-head">
          <h2>Current config.yaml</h2>
          <span className="muted small">as read by the last plan</span>
        </div>
        {stack.config ? (
          <pre className="code-block">
            {`version: ${stack.config.version}\nengine: ${stack.config.engine}\nengine_version: ${stack.config.engine_version}` +
              (stack.config.terragrunt_version ? `\nterragrunt_version: ${stack.config.terragrunt_version}` : "") +
              (stack.config.run_all ? "\nrun_all: true" : "")}
          </pre>
        ) : (
          <p className="muted">Not read yet: the first plan reads it from {stack.path}/.firstlume/config.yaml.</p>
        )}
        <p className="hint">To change the engine or its version, change this file in Git: the next run picks it up.</p>
      </section>

      <StackDeclaration stack={stack} />

      <section className="card danger-card">
        <div>
          <h2 className="danger-text">Delete stack</h2>
          <p className="muted">
            Removes the stack, its runs and its settings from First Lume. It does <strong>not</strong> destroy the infrastructure: run a destroy first if
            you want it gone.
          </p>
          {del.error && <div className="form-error">{del.error}</div>}
        </div>
        <button
          className="danger"
          disabled={del.busy}
          onClick={() => {
            if (!confirm(`Delete stack “${stack.name}”? This does not destroy its infrastructure.`)) return;
            void del.run(async () => {
              await api.deleteStack(stack.id);
              await onSaved().catch(() => {});
              navigate(stack.folder_id ? link(`f/${stack.folder_id}`) : link());
            });
          }}
        >
          Delete stack
        </button>
      </section>
    </div>
  );
}
