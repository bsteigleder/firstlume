import { useState } from "react";
import { Link, useParams } from "react-router";
import { settingsPath, type Stack } from "../api";
import { PeopleIcon, PlayIcon } from "../icons";
import { folderContents, PageTitle, useCrumbs, useOrg } from "../org";
import { DriftBadge, driftLabel, Empty, repoLabel, StatusBadge, timeAgo, useData } from "../ui";
import { StackGrip } from "./MoveStack";
import { RunDialog } from "./RunDialog";

/** A folder, or the organization's top level when no folder is in the URL. */
export function FolderPage() {
  const { folderId = "" } = useParams();
  const { org, tree, api, link, open, folderChain, drag } = useOrg();
  const [running, setRunning] = useState<Stack | null>(null);
  const chain = folderChain(folderId);
  const folder = chain[chain.length - 1];
  const settings = useData(() => api.getSettings(settingsPath(folderId ? { kind: "folder", id: folderId } : { kind: "organization" })), [api, folderId]);

  useCrumbs([{ label: org.name, to: link() }, ...chain.map((f) => ({ label: f.name, to: link(`f/${f.id}`) }))]);

  if (!tree) return null;
  if (folderId && !folder) return <Empty>This folder does not exist anymore.</Empty>;

  const stacks = tree.stacks.filter((s) => (s.folder_id ?? "") === folderId);
  const drift = settings.data?.effective.drift_interval_minutes.value ?? 0;
  const subfolders = tree.folders.filter((f) => (f.parent_id ?? "") === folderId).length;
  const contents = folderContents(tree, folderId);

  return (
    <div className="page">
      <PageTitle
        title={folder ? folder.name : org.name}
        sub={
          <>
            {stacks.length} stack{stacks.length === 1 ? "" : "s"}
            {drift > 0 && <> · drift check {driftLabel(drift).toLowerCase()}</>}
          </>
        }
        actions={
          <>
            {folder && (
              <>
                <button onClick={() => open({ kind: "rename-folder", folder })}>Rename</button>
                {contents.empty ? (
                  <button className="danger-text" onClick={() => open({ kind: "delete-folder", folder })}>
                    Delete
                  </button>
                ) : (
                  <button aria-disabled="true" className="tip tip-wrap"
                    data-tip={`Only empty folders can be deleted. ${folder.name} still has ${contents.summary}: move or delete them first.`}>
                    Delete
                  </button>
                )}
              </>
            )}
            <button className="primary" onClick={() => open({ kind: "new-stack", folderID: folderId })}>
              New stack
            </button>
          </>
        }
      />

      {stacks.length === 0 ? (
        <Empty>
          {subfolders > 0 ? "No stacks directly here. Pick a folder in the tree, or " : "No stacks here yet. "}
          <button className="link" onClick={() => open({ kind: "new-stack", folderID: folderId })}>
            create a stack
          </button>
          .
        </Empty>
      ) : (
        <div className="table-box">
          <table className="table">
            <thead>
              <tr>
                <th>Stack</th>
                <th>Last run</th>
                <th>Drift</th>
                <th>Engine</th>
                <th>
                  <span className="sr-only">Run</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {stacks.map((s) => {
                const r = s.last_run;
                return (
                  <tr key={s.id} className={drag?.id === s.id ? "dragging" : ""}>
                    <td>
                      <div className="stack-cell">
                        <StackGrip stack={s} rowSelector="tr" />
                        <div>
                          <Link className="row-title" to={link(`s/${s.id}`)}>
                            {s.name}
                          </Link>
                          <div className="mono muted small">
                            {repoLabel(s.repository)} · {s.path} @ {s.ref}
                          </div>
                        </div>
                      </div>
                    </td>
                    <td>
                      {r ? (
                        <>
                          <Link to={link(`r/${r.id}`)} className="plain">
                            <StatusBadge status={r.status} run={r} />
                          </Link>
                          <div className="muted small">
                            {r.commit_author_name ? `${r.commit_author_name} · ` : ""}
                            {r.commit_message ? `“${r.commit_message}” · ` : `${r.type} · `}
                            {timeAgo(r.created_at)}
                          </div>
                          {r.status === "awaiting_approval" && (
                            <div className="approvers small" title="Everyone in the organization is an administrator for now">
                              <PeopleIcon /> Can approve: <strong>organization administrators</strong>
                            </div>
                          )}
                        </>
                      ) : (
                        <span className="muted">Never run</span>
                      )}
                    </td>
                    <td>
                      {s.drift?.run_id ? (
                        <Link to={link(`r/${s.drift.run_id}`)} className="plain">
                          <DriftBadge drift={s.drift} />
                        </Link>
                      ) : (
                        <DriftBadge drift={s.drift} />
                      )}
                    </td>
                    <td className="mono small">
                      {s.config ? (s.config.run_all ? `terragrunt run --all` : `${s.config.engine} ${s.config.engine_version}`) : "—"}
                    </td>
                    <td className="right">
                      <button className="small-btn" onClick={() => setRunning(s)} aria-label={`Run ${s.name}`}>
                        <PlayIcon size={11} /> Run…
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {running && <RunDialog stack={running} onClose={() => setRunning(null)} />}
    </div>
  );
}
