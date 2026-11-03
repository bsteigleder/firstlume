import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { terminalStatuses, type StackResources, type Stack, type StateUnit } from "../api";
import { useOrg } from "../org";
import { Modal, timeAgo, useAction, useData } from "../ui";

// Exporting a stack's state, on its own or before deleting the stack (canvas
// boards 54 and 55; spec 0002, R6.3 and R8.9).

/** The units that have a state to export; a unit can exist with only a lock. */
export function unitsWithState(units: StateUnit[]): StateUnit[] {
  return units.filter((u) => u.version > 0);
}

function unitName(key: string): string {
  return key || "(root)";
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

/** The whole stack is a bundle; `unit` is a plain state. */
function exportFileName(stack: Stack, unit?: string): string {
  const safe = (s: string) => s.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
  const now = new Date();
  const date = [now.getFullYear(), String(now.getMonth() + 1).padStart(2, "0"), String(now.getDate()).padStart(2, "0")].join("-");
  const parts = [safe(stack.name), ...(unit ? [safe(unit)] : []), "state", date].join("-");
  return parts + (unit === undefined ? ".tar.gz" : ".tfstate");
}

/** Hands the browser a file that has already arrived whole. */
function saveFile(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

/** Says which states First Lume doesn't hold, so they can't be exported or deleted here. */
function ElsewhereNote({ resources, after }: { resources: StackResources | null; after: string }) {
  const outside = resources?.outside?.units ?? [];
  if (outside.length > 0) {
    const backends = [...new Set(outside.map((u) => u.backend))].join(", ");
    return (
      <p className="notice">
        <strong>
          {outside.length === 1 ? "1 unit keeps its" : `${outside.length} units keep their`} state elsewhere ({backends}):
        </strong>{" "}
        {outside.map((u, i) => (
          <span key={u.unit}>
            {i > 0 && ", "}
            <span className="mono">{unitName(u.unit)}</span>
          </span>
        ))}
        . {after}
      </p>
    );
  }
  if (resources?.state_outside) {
    return (
      <p className="notice">
        <strong>This stack keeps its state elsewhere.</strong> {after}
      </p>
    );
  }
  return null;
}

export function ExportStateDialog({ stack, units, onClose }: { stack: Stack; units: StateUnit[]; onClose: () => void }) {
  const { api, link, notify } = useOrg();
  const runs = useData(() => api.listRuns(stack.id), [api, stack.id]);
  const resources = useData(() => api.resources(stack.id), [api, stack.id]);
  const single = units.length === 1;
  const [whole, setWhole] = useState(!single);
  const [key, setKey] = useState(units[0].key);
  const { busy, error, run } = useAction();

  const chosen = units.find((u) => u.key === key) ?? units[0];
  const unit = whole ? undefined : chosen.key;
  const fileName = exportFileName(stack, unit);
  const applying = runs.data?.find((r) => r.status === "applying");
  const total = units.reduce((n, u) => n + u.size, 0);
  const resourceCount = units.reduce((n, u) => n + u.resources, 0);

  function download() {
    return run(async () => {
      saveFile(await api.exportState(stack.id, unit), fileName);
      notify(`Downloaded ${fileName}`);
      onClose();
    });
  }

  return (
    <Modal title={`Export state of “${stack.name}”`} onClose={onClose}>
      <div className="modal-body">
        {single ? (
          <p className="hint">
            {plural(chosen.resources, "resource")} · {formatSize(chosen.size)}
          </p>
        ) : (
          <fieldset className="choices">
            <legend className="sr-only">What to export</legend>
            <label className={`choice${whole ? " selected" : ""}`}>
              <input type="radio" name="export-scope" checked={whole} onChange={() => setWhole(true)} />
              <span>
                <strong>All units</strong>
                <span className="hint">
                  {plural(units.length, "unit")} · {plural(resourceCount, "resource")} · {formatSize(total)}
                </span>
              </span>
            </label>
            <label className={`choice${whole ? "" : " selected"}`}>
              <input type="radio" name="export-scope" checked={!whole} onChange={() => setWhole(false)} />
              <span className="grow">
                <strong>One unit</strong>
                {!whole && (
                  <select className="mono export-unit" aria-label="Unit" value={chosen.key} onChange={(e) => setKey(e.target.value)}>
                    {units.map((u) => (
                      <option key={u.key} value={u.key}>
                        {unitName(u.key)} · {plural(u.resources, "resource")} · {formatSize(u.size)}
                      </option>
                    ))}
                  </select>
                )}
              </span>
            </label>
          </fieldset>
        )}

        <div className="export-file">
          <span className="mono">{fileName}</span>
          {whole ? (
            <span className="hint">
              One <span className="mono">&lt;unit&gt;/terraform.tfstate</span> per unit. Import state reads this same layout.
            </span>
          ) : (
            <span className="hint">
              Version {chosen.version}
              {chosen.updated_at && <>, written {timeAgo(chosen.updated_at)}</>}
              {chosen.run_id ? (
                <>
                  {" "}by <Link to={link(`r/${chosen.run_id}`)}>run {chosen.run_id.slice(0, 8)}</Link>
                </>
              ) : (
                chosen.written_by && <> by {chosen.written_by}</>
              )}
              .
            </span>
          )}
        </div>

        {applying && (
          <p className="notice">
            <strong>A run is applying now.</strong> You get the last state written, which may not have what this run is changing.{" "}
            <Link to={link(`r/${applying.id}`)}>See run {applying.id.slice(0, 8)}</Link>
          </p>
        )}
        <ElsewhereNote resources={resources.data} after="They aren’t in this file, since First Lume doesn’t hold them." />
        <p className="notice notice-attention">
          <strong>This file holds secrets in plain text.</strong> State files keep passwords, keys and tokens, and the export is decrypted. Keep it
          somewhere safe.
        </p>
        {error && (
          <p className="notice notice-danger" role="alert">
            <strong>Nothing was downloaded:</strong> {error}.{whole && !single ? " Export the other units one at a time, or try again." : " Try again."}
          </p>
        )}

        <div className="form-actions export-actions">
          <span className="hint grow">Who exported and when is recorded.</span>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy} onClick={download}>
            {busy ? "Downloading…" : "Download"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

/** The optional "keep a copy" row of the delete dialog: it never blocks deleting. */
function ExportRow({ stack, units, hint }: { stack: Stack; units: StateUnit[]; hint: string }) {
  const { api } = useOrg();
  const { busy, error, run } = useAction();
  const [downloaded, setDownloaded] = useState("");
  const unit = units.length === 1 ? units[0].key : undefined;

  function exportNow() {
    return run(async () => {
      const name = exportFileName(stack, unit);
      saveFile(await api.exportState(stack.id, unit), name);
      setDownloaded(name);
    });
  }

  return (
    <div className={`export-row${downloaded ? " done" : ""}`}>
      <div className="export-row-text">
        {downloaded ? (
          <>
            <strong>Downloaded</strong>
            <span className="mono">{downloaded}</span>
          </>
        ) : (
          <>
            <strong>Keep a copy of the state</strong>
            <span className="hint">{hint}</span>
          </>
        )}
        {error && (
          <span className="form-error" role="alert">
            Nothing was downloaded: {error}.
          </span>
        )}
      </div>
      <button type="button" disabled={busy} onClick={exportNow}>
        {busy ? "Exporting…" : downloaded ? "Export again" : "Export state"}
      </button>
    </div>
  );
}

export function DeleteStackDialog({ stack, onClose, onDestroy }: { stack: Stack; onClose: () => void; onDestroy: () => void }) {
  const { api, link, reloadTree } = useOrg();
  const navigate = useNavigate();
  const allUnits = useData(() => api.stateUnits(stack.id), [api, stack.id]);
  const runs = useData(() => api.listRuns(stack.id), [api, stack.id]);
  const resources = useData(() => api.resources(stack.id), [api, stack.id]);
  const [typed, setTyped] = useState("");
  const { busy, error, run } = useAction();

  const title = `Delete stack “${stack.name}”?`;
  const loadError = allUnits.error ?? runs.error;
  if (!allUnits.data || !runs.data) {
    return (
      <Modal title={title} onClose={onClose}>
        <div className="modal-body">
          {loadError ? <p className="notice notice-danger">{loadError}</p> : <p className="hint">Checking its state and runs…</p>}
        </div>
      </Modal>
    );
  }

  const units = unitsWithState(allUnits.data);
  const hasState = units.length > 0;
  const managed = units.reduce((n, u) => n + u.resources, 0);
  const size = formatSize(units.reduce((n, u) => n + u.size, 0));
  const activeRun = runs.data.find((r) => !terminalStatuses.includes(r.status));
  const locked = allUnits.data.find((u) => u.lock);
  const blocked = !!activeRun || !!locked;
  const ready = !blocked && (managed === 0 || typed === stack.name);

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!ready) return;
    void run(async () => {
      await api.deleteStack(stack.id, managed > 0 ? stack.name : undefined);
      await reloadTree().catch(() => {});
      navigate(stack.folder_id ? link(`f/${stack.folder_id}`) : link());
    });
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form className="modal-body" onSubmit={submit}>
        {activeRun ? (
          <p className="notice notice-danger">
            <strong>A run is in progress.</strong> The stack can’t be deleted until{" "}
            <Link to={link(`r/${activeRun.id}`)}>run {activeRun.id.slice(0, 8)}</Link>{" "}
            {activeRun.status === "awaiting_approval" ? "is approved or discarded" : "finishes"}.
          </p>
        ) : locked?.lock ? (
          <p className="notice notice-danger">
            <strong>
              Unit <span className="mono">{unitName(locked.key)}</span> is locked
            </strong>{" "}
            by {locked.lock.holder}, {timeAgo(locked.lock.since)}. The stack can’t be deleted until it is unlocked.
          </p>
        ) : (
          managed > 0 && (
            <p className="notice notice-danger">
              <strong>
                Its state still manages {plural(managed, "resource")} in {plural(units.length, "unit")}.
              </strong>{" "}
              That infrastructure keeps running, with nothing in First Lume managing it. To remove it too, cancel and{" "}
              <button type="button" className="link-inline" onClick={onDestroy}>
                run a destroy
              </button>{" "}
              first.
            </p>
          )
        )}

        {hasState && (
          <ExportRow
            stack={stack}
            units={units}
            hint={
              activeRun
                ? "The last state written. It may not have what this run is changing."
                : managed > 0
                  ? `Optional. ${size}, secrets in plain text.`
                  : `Optional. ${plural(units.length, "unit")}, ${size}.`
            }
          />
        )}

        {!blocked &&
          (managed > 0 ? (
            <p>The stack’s runs, settings and state are deleted. The stored state files are erased after 30 days, and no screen brings them back.</p>
          ) : hasState ? (
            <p>Removes the stack, its runs and its settings from First Lume. Its state has no resources left and is deleted too.</p>
          ) : (
            <p>
              Removes the stack, its runs and its settings from First Lume.
              {!resources.data?.outside?.units.length && !resources.data?.state_outside && " It has no state here."}
            </p>
          ))}

        {!blocked && <ElsewhereNote resources={resources.data} after="Those states stay where they are, untouched." />}

        {!blocked && managed > 0 && (
          <label className="field">
            Type {stack.name} to confirm
            <input className="mono" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={stack.name} autoFocus autoComplete="off" />
          </label>
        )}

        {error && <div className="form-error">{error}</div>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            {blocked ? "Close" : "Cancel"}
          </button>
          <button className="danger" disabled={!ready || busy}>
            {busy ? "Deleting…" : hasState ? "Delete stack and state" : "Delete stack"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
