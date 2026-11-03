import { useMemo, useRef, useState, type DragEvent } from "react";
import type { IdentifiedState, Stack, StateIdentifyResult, StateImportResult, StatePlacement } from "../api";
import { useOrg } from "../org";
import { Modal, errorMessage, useAction } from "../ui";

/** Importing existing states: First Lume says where each goes, a person confirms (spec 0002 R9.7 to R9.16). */

/** What a person decided for one state, over what was suggested. */
type Choice = { unit: string } | { skip: true };

export interface ImportSummary {
  states: number;
  resources: number;
}

const unitLabel = (key: string) => key || "(whole stack)";

/** The state of one import: its files, what identifying said, and the choices. */
export function useImportPlan(identify: (files: File[]) => Promise<StateIdentifyResult>) {
  const [files, setFiles] = useState<File[]>([]);
  const [result, setResult] = useState<StateIdentifyResult | null>(null);
  const [choices, setChoices] = useState<Record<string, Choice>>({});
  const [anyway, setAnyway] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function change(next: File[]) {
    setFiles(next);
    setAnyway(false);
    setError(null);
    if (next.length === 0) {
      setResult(null);
      setChoices({});
      return;
    }
    setBusy(true);
    try {
      setResult(await identify(next));
    } catch (e) {
      setResult(null);
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const states = useMemo(() => result?.states ?? [], [result]);
  const units = useMemo(() => result?.units ?? [], [result]);
  // Nothing fits the code: maybe the wrong folder or branch (R9.14).
  const nothingMatches = units.length > 0 && states.length > 0 && states.every((s) => !s.placed && (s.why === "no_match" || s.why === "contradicts"));

  /** Where a state goes now, or undefined while nobody has decided. */
  const decided = (s: IdentifiedState): Choice | undefined => {
    if (choices[s.source]) return choices[s.source];
    if (s.placed) return { unit: s.unit };
    if (nothingMatches && anyway) return { unit: s.path_unit };
    return undefined;
  };

  const placements: StatePlacement[] = [];
  const taken = new Map<string, number>();
  let undecided = 0;
  let resources = 0;
  let importing = 0;
  for (const s of states) {
    const c = decided(s);
    if (!c) {
      undecided++;
      placements.push({ source: s.source, unit: "", skip: true });
    } else if ("skip" in c) {
      placements.push({ source: s.source, unit: "", skip: true });
    } else {
      placements.push({ source: s.source, unit: c.unit });
      taken.set(c.unit, (taken.get(c.unit) ?? 0) + 1);
      resources += s.resources;
      importing++;
    }
  }
  const duplicates = [...taken.entries()].filter(([, n]) => n > 1).map(([u]) => u);
  const problems = result?.problems ?? [];
  const ready = !busy && importing > 0 && undecided === 0 && duplicates.length === 0 && problems.length === 0;

  let hint = "Nothing is saved yet.";
  if (undecided > 0) hint += ` ${undecided} state${undecided === 1 ? "" : "s"} left to place.`;
  if (duplicates.length > 0) hint = `Two states go to ${unitLabel(duplicates[0])}: choose one of them.`;

  return {
    files,
    result,
    states,
    units,
    busy,
    error,
    change,
    anyway,
    setAnyway,
    nothingMatches,
    decided,
    taken,
    choose: (source: string, c: Choice | undefined) =>
      setChoices((cur) => {
        const next = { ...cur };
        if (c) next[source] = c;
        else delete next[source];
        return next;
      }),
    placements,
    ready,
    hint,
    summary: { states: importing, resources } as ImportSummary,
  };
}

type Plan = ReturnType<typeof useImportPlan>;

function howWeKnow(s: IdentifiedState, chosen: boolean): string {
  if (chosen) return "Chosen by you";
  switch (s.reason) {
    case "lineage":
      return "It’s the same state this unit already has";
    case "path":
      return "Same path in the file";
    case "resources":
      if (s.matched < s.total) return `${s.matched} of ${s.total} resources are in this folder’s code`;
      return s.total === 1 ? "Its resource is in this folder’s code" : `All ${s.total} resources are in this folder’s code`;
    case "values":
      return "Same code as another folder, but its names match this folder’s values";
    case "only_unit":
      return "The stack has a single state";
    default:
      return "";
  }
}

function whyUnplaced(s: IdentifiedState): string {
  const c = (s.candidates ?? []).map(unitLabel);
  switch (s.why) {
    case "tie":
      return `It fits ${c.join(" and ")} equally: they use the same code.`;
    case "contradicts":
      return `Its path points to ${c[0]}, but its resources aren’t in that folder’s code.`;
    case "older_copy":
      return `It’s an older copy of the state placed in ${c[0]}.`;
    case "competing":
      return `Another uploaded state, with a different history, also fits ${c[0]}.`;
    default:
      return "Its resources match no folder here.";
  }
}

/** The files, then where each state goes (canvas boards 50 and 51). */
export function ImportPlan({ plan, rootLabel, onPickAnotherFolder }: { plan: Plan; rootLabel: string; onPickAnotherFolder?: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const add = (list: FileList | null) => {
    if (list && list.length > 0) void plan.change([...plan.files, ...Array.from(list)]);
  };
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    add(e.dataTransfer.files);
  };
  const { result, states } = plan;
  const problems = result?.problems ?? [];
  const placeable = states.filter((s) => plan.decided(s) === undefined || "skip" in (plan.decided(s) ?? {}));
  const placed = states.filter((s) => {
    const c = plan.decided(s);
    return c && !("skip" in c);
  });

  return (
    <div className="import-plan">
      <div
        className={`import-drop${dragging ? " dragging" : ""}`}
        onDragOver={(e) => (e.preventDefault(), setDragging(true))}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        <input
          ref={input}
          type="file"
          multiple
          hidden
          aria-label="State files"
          onChange={(e) => {
            add(e.target.files);
            e.target.value = "";
          }}
        />
        <span className="grow">
          {plan.files.length === 0 ? (
            <>
              <strong>Drop the states here</strong>
              <span className="muted small">
                <span className="mono">.tfstate</span> files or a <span className="mono">tar.gz</span>, in any folder layout.
              </span>
            </>
          ) : (
            <>
              <strong>{plan.busy ? "Reading…" : `${states.length} state${states.length === 1 ? "" : "s"}`}</strong>
              <span className="muted small mono">{plan.files.map((f) => f.name).join(", ")}</span>
            </>
          )}
        </span>
        {plan.files.length > 0 && (
          <button type="button" className="link" onClick={() => void plan.change([])}>
            Start over
          </button>
        )}
        <button type="button" onClick={() => input.current?.click()}>
          {plan.files.length === 0 ? "Choose files" : "Add more files"}
        </button>
      </div>

      {plan.error && <div className="form-error">{plan.error}</div>}

      {problems.length > 0 && (
        <div className="import-section">
          <div className="notice notice-danger">
            <strong>Nothing can be imported yet.</strong> A bundle is imported whole or not at all, so fix these and upload it again.
          </div>
          <div className="import-table">
            <table>
              <thead>
                <tr>
                  <th>File</th>
                  <th>Problem</th>
                </tr>
              </thead>
              <tbody>
                {problems.map((p) => (
                  <tr key={p.path + p.error}>
                    <td className="mono">{p.path}</td>
                    <td>{p.error}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {result && states.length > 0 && (
        <div className="import-section">
          {result.code_note && <div className="notice">{cap(result.code_note)}. Type the unit of each state if it isn’t right.</div>}
          {plan.nothingMatches && (
            <div className="notice notice-attention import-nothing">
              <span className="grow">
                <strong>
                  None of these states matches the code in <span className="mono">{rootLabel}</span>.
                </strong>{" "}
                Neither the paths nor the resources fit, so this may be the wrong folder or branch. A plan would try to create everything again.
              </span>
              {onPickAnotherFolder && (
                <button type="button" onClick={onPickAnotherFolder}>
                  Pick another folder
                </button>
              )}
              <label className="check">
                <input type="checkbox" checked={plan.anyway} onChange={(e) => plan.setAnyway(e.target.checked)} />
                Import anyway, under these paths
              </label>
            </div>
          )}
          <div className="section-head">
            <h3 className="wizard-title">Where each state goes</h3>
            <span className="muted small">
              {placed.length} placed{placeable.length > 0 ? ` · ${placeable.length} ${placeable.length === 1 ? "needs" : "need"} you` : ""}
            </span>
          </div>
          {plan.units.length > 0 ? <PlacementTree plan={plan} rootLabel={rootLabel} /> : <TypedUnits plan={plan} />}
          {plan.units.length > 0 && placeable.length > 0 && (
            <div className="import-unplaced">
              <h4>We couldn’t tell where {placeable.length === 1 ? "this one goes" : "these go"}</h4>
              {placeable.map((s) => (
                <div key={s.source} className="notice notice-attention import-question">
                  <span className="grow">
                    <span>
                      <span className="mono">{s.source}</span> · {s.resources} resource{s.resources === 1 ? "" : "s"}
                    </span>
                    <span className="small">{whyUnplaced(s)}</span>
                    <AlreadyIn s={s} />
                  </span>
                  <label className="inline-field">
                    Goes to <UnitSelect plan={plan} s={s} />
                  </label>
                </div>
              ))}
            </div>
          )}
          {(result.ignored?.length ?? 0) > 0 && (
            <details className="import-ignored">
              <summary>
                {result.ignored?.length} file{result.ignored?.length === 1 ? "" : "s"} ignored
              </summary>
              <pre className="code-block">{result.ignored?.join("\n")}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

function AlreadyIn({ s }: { s: IdentifiedState }) {
  if (!s.already_in?.length) return null;
  const at = s.already_in[0];
  return (
    <span className="small import-also">
      Stack “{at.stack}” already has this state ({unitLabel(at.unit)}). Two stacks would write to the same infrastructure.
    </span>
  );
}

function UnitSelect({ plan, s }: { plan: Plan; s: IdentifiedState }) {
  const c = plan.decided(s);
  const value = !c ? "" : "skip" in c ? "\u0000skip" : `=${c.unit}`;
  return (
    <select
      aria-label={`Where ${s.source} goes`}
      value={value}
      onChange={(e) => {
        const v = e.target.value;
        plan.choose(s.source, v === "" ? undefined : v === "\u0000skip" ? { skip: true } : { unit: v.slice(1) });
      }}
    >
      <option value="">Choose a folder…</option>
      {plan.units.map((u) => {
        const usedByOther = (plan.taken.get(u) ?? 0) > 0 && !(c && "unit" in c && c.unit === u);
        return (
          <option key={u} value={`=${u}`} disabled={usedByOther}>
            {unitLabel(u)}
          </option>
        );
      })}
      <option value={"\u0000skip"}>Leave it out</option>
    </select>
  );
}

type Row =
  | { kind: "folder"; name: string; depth: number; key: string }
  | { kind: "unit"; name: string; depth: number; key: string; state?: IdentifiedState; hasCode: boolean };

/** The repository's folders with the states placed in them (board 50). */
function PlacementTree({ plan, rootLabel }: { plan: Plan; rootLabel: string }) {
  const [changing, setChanging] = useState<Record<string, boolean>>({});
  const byUnit = new Map<string, IdentifiedState>();
  for (const s of plan.states) {
    const c = plan.decided(s);
    if (c && "unit" in c && !byUnit.has(c.unit)) byUnit.set(c.unit, s);
  }
  const keys = [...new Set([...plan.units, ...byUnit.keys()])].sort();
  const rows: Row[] = [];
  const opened = new Set<string>();
  for (const key of keys) {
    const parts = key ? key.split("/") : [""];
    parts.slice(0, -1).forEach((name, i) => {
      const folder = parts.slice(0, i + 1).join("/");
      if (!opened.has(folder) && !keys.includes(folder)) {
        opened.add(folder);
        rows.push({ kind: "folder", name, depth: i + 1, key: folder });
      }
    });
    rows.push({ kind: "unit", name: key ? parts[parts.length - 1] : "(whole stack)", depth: parts.length, key, state: byUnit.get(key), hasCode: plan.units.includes(key) });
  }

  return (
    <div className="import-table">
      <table>
        <thead>
          <tr>
            <th>Folder in the repository</th>
            <th className="num">Resources</th>
            <th>How we know</th>
            <th aria-label="Change" />
          </tr>
        </thead>
        <tbody>
          <tr>
            <td colSpan={4}>
              <span className="import-node">
                <FolderGlyph />
                <span className="mono">{rootLabel}</span>
              </span>
            </td>
          </tr>
          {rows.map((r) =>
            r.kind === "folder" ? (
              <tr key={`f:${r.key}`}>
                <td colSpan={4}>
                  <span className="import-node" style={{ paddingLeft: r.depth * 20 }}>
                    <FolderGlyph />
                    <span className="mono">{r.name}</span>
                  </span>
                </td>
              </tr>
            ) : (
              <tr key={`u:${r.key}`} className={r.state ? "" : "import-empty"}>
                <td>
                  <span className="import-node" style={{ paddingLeft: r.depth * 20 }}>
                    <UnitGlyph />
                    <span className="mono">{r.name}</span>
                  </span>
                </td>
                <td className="num">{r.state?.resources ?? ""}</td>
                <td>
                  {r.state ? (
                    <span className="import-how">
                      <span>{r.hasCode ? howWeKnow(r.state, !!plan.decided(r.state) && !r.state.placed) : <span className="badge warn">No folder in the repository</span>}</span>
                      <span className="mono muted">{r.state.source}</span>
                      <AlreadyIn s={r.state} />
                    </span>
                  ) : (
                    <span className="muted">Has code, no state yet</span>
                  )}
                </td>
                <td className="import-change">
                  {r.state &&
                    (changing[r.state.source] ? (
                      <UnitSelect plan={plan} s={r.state} />
                    ) : (
                      <button type="button" className="link" onClick={() => setChanging((c) => ({ ...c, [r.state?.source ?? ""]: true }))}>
                        Change
                      </button>
                    ))}
                </td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}

/** Without code to compare with, the path is a suggestion a person edits (R9.12). */
function TypedUnits({ plan }: { plan: Plan }) {
  return (
    <div className="import-table">
      <table>
        <thead>
          <tr>
            <th>State</th>
            <th className="num">Resources</th>
            <th>Unit</th>
            <th aria-label="Leave it out" />
          </tr>
        </thead>
        <tbody>
          {plan.states.map((s) => {
            const c = plan.decided(s);
            const skipped = !!c && "skip" in c;
            return (
              <tr key={s.source}>
                <td>
                  <span className="import-how">
                    <span className="mono">{s.source}</span>
                    <AlreadyIn s={s} />
                  </span>
                </td>
                <td className="num">{s.resources}</td>
                <td>
                  <input
                    className="mono"
                    aria-label={`Unit of ${s.source}`}
                    disabled={skipped}
                    value={c && "unit" in c ? c.unit : ""}
                    placeholder="(whole stack)"
                    onChange={(e) => plan.choose(s.source, { unit: e.target.value.trim() })}
                  />
                </td>
                <td>
                  <label className="check">
                    <input type="checkbox" checked={skipped} onChange={(e) => plan.choose(s.source, e.target.checked ? { skip: true } : { unit: s.unit })} />
                    Leave out
                  </label>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function FolderGlyph() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden className="muted">
      <path d="M1.75 4.25a1 1 0 0 1 1-1h3.4l1.5 1.5h5.6a1 1 0 0 1 1 1v6.5a1 1 0 0 1-1 1H2.75a1 1 0 0 1-1-1z" />
    </svg>
  );
}

function UnitGlyph() {
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round" aria-hidden>
      <path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z" />
      <path d="M12 12l8-4.5M12 12v9M12 12L4 7.5" />
    </svg>
  );
}

const statusTone: Record<string, string> = { new: "ok", same: "idle", newer: "warn", older: "warn", conflict: "bad" };

function compareDetail(u: StateImportResult["units"][number]): string {
  switch (u.status) {
    case "new":
      return `${u.resources} resource${u.resources === 1 ? "" : "s"}, no state yet`;
    case "same":
      return `Identical to version ${u.current_version}`;
    case "conflict":
      return "Another lineage: it came from a different state";
    default:
      return `Serial ${u.serial}, current is ${u.current_serial}`;
  }
}

/** Import state into a stack that may already have some (board 52, part B). */
export function ImportStateDialog({ stack, onClose, onDone }: { stack: Stack; onClose: () => void; onDone: (summary: ImportSummary) => void }) {
  const { api } = useOrg();
  const plan = useImportPlan((files) => api.identifyForStack(stack.id, files));
  const [compared, setCompared] = useState<StateImportResult | null>(null);
  const [replace, setReplace] = useState<Record<string, boolean>>({});
  const compare = useAction();
  const save = useAction();
  const marked = Object.keys(replace).filter((k) => replace[k]);
  const writes = compared?.units.filter((u) => u.action === "create" || replace[u.key]) ?? [];

  async function runCompare() {
    await compare.run(async () => {
      setCompared(await api.importState(stack.id, plan.files, plan.placements, [], true));
    });
  }

  async function runImport() {
    await save.run(async () => {
      const done = await api.importState(stack.id, plan.files, plan.placements, marked, false);
      const wrote = done.units.filter((u) => u.action !== "skip");
      onDone({ states: wrote.length, resources: wrote.reduce((n, u) => n + u.resources, 0) });
    });
  }

  return (
    <Modal title={`Import state into ${stack.name}`} onClose={onClose} wide>
      <div className="modal-body">
        {!compared ? (
          <>
            <ImportPlan plan={plan} rootLabel={stack.path} />
            {compare.error && <div className="form-error">{compare.error}</div>}
            <div className="form-actions">
              <button type="button" onClick={onClose}>
                Cancel
              </button>
              <span className="muted small grow-right">{plan.files.length > 0 ? plan.hint : ""}</span>
              <button className="primary" disabled={!plan.ready || compare.busy} onClick={() => void runCompare()}>
                {compare.busy ? "Comparing…" : "Continue"}
              </button>
            </div>
          </>
        ) : (
          <>
            <p className="muted">What already has state stays as it is unless you mark it. Replacing keeps the old version in the history.</p>
            <div className="import-table">
              <table>
                <thead>
                  <tr>
                    <th>Unit</th>
                    <th>Compared with the current state</th>
                    <th>What happens</th>
                  </tr>
                </thead>
                <tbody>
                  {compared.units.map((u) => (
                    <tr key={u.key}>
                      <td className="mono">{unitLabel(u.key)}</td>
                      <td>
                        <span className="import-compare">
                          <span className={`badge ${statusTone[u.status]}`}>{cap(u.status)}</span>
                          <span className="muted small">{compareDetail(u)}</span>
                        </span>
                      </td>
                      <td>
                        {u.locked_by ? (
                          <span className="muted">Locked by {u.locked_by}</span>
                        ) : u.status === "new" ? (
                          "Imported"
                        ) : u.status === "same" ? (
                          "Nothing"
                        ) : (
                          <label className="check">
                            <input type="checkbox" checked={!!replace[u.key]} onChange={(e) => setReplace((r) => ({ ...r, [u.key]: e.target.checked }))} />
                            Replace
                          </label>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {save.error && <div className="form-error">{save.error}</div>}
            <div className="form-actions">
              <button type="button" onClick={() => (setCompared(null), setReplace({}))}>
                Back
              </button>
              <span className="grow-right" />
              <button className="primary" disabled={writes.length === 0 || save.busy} onClick={() => void runImport()}>
                {save.busy ? "Importing…" : `Import ${writes.length} unit${writes.length === 1 ? "" : "s"}`}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

/** What to do right after an import (board 52, part A; R9.15). */
export function ImportedNotice({ summary, onRunPlan, onDismiss }: { summary: ImportSummary; onRunPlan: () => void; onDismiss: () => void }) {
  return (
    <div className="import-done">
      <div className="notice notice-success import-question">
        <span className="grow">
          <strong>
            Imported {summary.states} state{summary.states === 1 ? "" : "s"}, {summary.resources} resource{summary.resources === 1 ? "" : "s"}.
          </strong>{" "}
          Run a plan to confirm: it should say “No changes” in every unit.
        </span>
        <button type="button" className="primary" onClick={onRunPlan}>
          Run plan
        </button>
        <button type="button" className="link" onClick={onDismiss}>
          Dismiss
        </button>
      </div>
      <div className="notice notice-attention">
        <strong>Before the first plan.</strong> Stop applies in the old pipeline, so only one place writes to this state. The code must declare{" "}
        <span className="mono">backend "http" {"{}"}</span>; First Lume fills in the address and the credentials.
      </div>
    </div>
  );
}
