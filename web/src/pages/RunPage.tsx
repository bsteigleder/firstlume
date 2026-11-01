import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import {
  changeKey,
  isTerminal,
  settingsPath,
  streamLogs,
  type ChangeAction,
  type Level,
  type LogEvent,
  type ResourceChange,
  type Run,
  type RunStep,
  type Stack,
} from "../api";
import { ChevronRightIcon, ExternalIcon } from "../icons";
import { useCrumbs, useOrg } from "../org";
import {
  commitURL,
  duration,
  ErrorBanner,
  fileURL,
  humanType,
  Initials,
  levelName,
  StatusBadge,
  onTabShown,
  timeAgo,
  useAction,
  useData,
} from "../ui";
import { ResourceIcon } from "../resourceIcons";
import { LabelLine } from "../resourceLabel";
import { RunDialog } from "./RunDialog";

export function RunPage() {
  const { runId = "" } = useParams();
  const { api, org, link, folderChain, reloadActive } = useOrg();
  const run = useData(() => api.getRun(runId), [api, runId]);
  const status = run.data?.status;
  const active = !!status && !isTerminal(status);
  const stack = useData(async () => (run.data ? api.getStack(run.data.stack_id) : null), [api, run.data?.stack_id]);
  const steps = useData(() => api.steps(runId), [api, runId]);
  const changes = useData(() => api.changes(runId), [api, runId, status]);
  const settings = useData(async () => (run.data ? api.getSettings(settingsPath({ kind: "stack", id: run.data.stack_id })) : null), [api, run.data?.stack_id]);
  const logs = useLogs(api.logsURL(runId), run.data, run.reload);
  const action = useAction();
  const [acked, setAcked] = useState<Record<string, boolean>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [reconcile, setReconcile] = useState(false);

  // Keep the run and its steps fresh while they can still change.
  useEffect(() => {
    if (!active) return;
    const refresh = () => {
      void run.reload();
      void steps.reload();
    };
    const t = setInterval(refresh, 2000);
    const stop = onTabShown(refresh);
    return () => {
      clearInterval(t);
      stop();
    };
  }, [active, run.reload, steps.reload]);
  useEffect(() => {
    void steps.reload();
  }, [status, steps.reload]);

  const chain = folderChain(stack.data?.folder_id ?? undefined);
  useCrumbs([
    { label: org.name, to: link() },
    ...chain.map((f) => ({ label: f.name, to: link(`f/${f.id}`) })),
    ...(stack.data ? [{ label: stack.data.name, to: link(`s/${stack.data.id}`) }] : []),
    { label: `Run ${runId.slice(0, 8)}` },
  ]);

  if (run.error && !run.data) return <ErrorBanner error={run.error} />;
  if (!run.data) return null;
  const r = run.data;
  const s = stack.data;
  const track = buildTrack(r, steps.data ?? []);
  const current = selected ?? defaultStep(track);
  const confirmRequired = settings.data?.effective.confirm_destructive.value ?? false;
  const destructive = (changes.data ?? []).filter((c) => c.action === "delete" || c.action === "replace");
  const missing = confirmRequired ? destructive.filter((c) => !acked[changeKey(c)]) : [];
  const awaiting = r.status === "awaiting_approval";
  const isDrift = r.type === "drift";

  async function act(fn: () => Promise<Run>) {
    await action.run(async () => {
      await fn();
      await run.reload();
      void reloadActive();
    });
  }

  return (
    <div className="page run-page">
      <div className="run-head">
        <h1>
          {isDrift ? "Drift check" : r.type === "plan" ? "Plan" : r.type === "destroy" ? "Destroy" : "Apply"} · {s?.name ?? "…"}
        </h1>
        <StatusBadge status={r.status} />
        <CommitLine run={r} stack={s} />
        <span className="grow" />
        {(r.status === "queued" || r.status === "apply_queued") && (
          <button onClick={() => act(() => api.cancelRun(r.id))} disabled={action.busy}>
            Cancel run
          </button>
        )}
        {(r.status === "failed" || r.status === "discarded" || r.status === "canceled") && s && (
          <button className="primary" onClick={() => setReconcile(true)}>
            Run again
          </button>
        )}
      </div>
      <ErrorBanner error={action.error} onDismiss={() => action.setError(null)} />

      {r.status === "failed" && r.error && (
        <div className="error-banner" role="alert">
          <span>
            <strong>{track.find((t) => t.state === "failed")?.name ?? "The run"} failed:</strong> {r.error}
          </span>
        </div>
      )}
      {r.status === "drifted" && (
        <div className="banner banner-attention static">
          <span className="banner-text">
            <strong>The real infrastructure no longer matches the code. Nothing was changed.</strong>
            <br />
            Fix the code to keep what changed, or reconcile to put the infrastructure back as the code says.
          </span>
          {s && (
            <button className="primary" onClick={() => setReconcile(true)}>
              Plan &amp; apply to reconcile
            </button>
          )}
        </div>
      )}

      <section className="card steps-card" aria-label="Run steps">
        <StepTrack track={track} selected={current} onSelect={setSelected} />
        <StepLog track={track} selected={current} events={logs.events} run={r} live={logs.streaming} error={logs.error} />
        <p className="hint">Click a step to see its log. Hooks from .firstlume will show up here as their own steps.</p>
      </section>

      {awaiting && (
        <section className="card approval-card" aria-label="Approval">
          <ActionCounts changes={changes.data ?? []} />
          <span className={missing.length ? "danger-text small" : "small muted"}>
            {confirmRequired && destructive.length > 0
              ? missing.length
                ? `Confirm ${missing.length} of ${destructive.length} destructive changes to approve`
                : "All destructive changes confirmed"
              : `Approving applies exactly this plan, from commit ${r.commit_sha?.slice(0, 7) ?? ""}.`}
          </span>
          <span className="grow" />
          <button onClick={() => act(() => api.discardRun(r.id))} disabled={action.busy}>
            Discard
          </button>
          <button
            className={r.type === "destroy" ? "danger" : "primary"}
            disabled={action.busy || missing.length > 0}
            onClick={() => act(() => api.approveRun(r.id, destructive.filter((c) => acked[changeKey(c)]).map(changeKey)))}
          >
            {r.type === "destroy" ? "Approve & destroy" : "Approve & apply"}
          </button>
        </section>
      )}

      <div className="run-columns">
        <section className="changes-col" aria-label="Changes">
          <h2>{isDrift ? "What differs from the code" : "What changes"}</h2>
          <ChangeList
            changes={changes.data}
            drift={isDrift}
            ack={awaiting && confirmRequired ? { acked, toggle: (k) => setAcked((a) => ({ ...a, [k]: !a[k] })), origin: settings.data?.effective.confirm_destructive.origin } : null}
            planning={r.status === "queued" || r.status === "planning"}
          />
        </section>
        <aside className="run-aside">
          <CommitCard run={r} stack={s} />
          {s && <ConfigCard run={r} stack={s} />}
        </aside>
      </div>
      {reconcile && s && <RunDialog stack={s} again={{ type: r.type, ref: r.ref }} onClose={() => setReconcile(false)} />}
    </div>
  );
}

// --- steps ---

type StepState = "done" | "running" | "waiting" | "pending" | "failed" | "skipped";

interface TrackItem {
  key: string;
  phase: "plan" | "approval" | "apply";
  name: string;
  state: StepState;
  meta: string;
  hook?: boolean;
}

/** The run's steps: the executor's, plus "Queued" and "Approval", which the server knows. */
function buildTrack(r: Run, steps: RunStep[]): TrackItem[] {
  const fromStep = (s: RunStep): TrackItem => ({
    key: `${s.phase}/${s.name}`,
    phase: s.phase,
    name: s.name,
    hook: s.kind === "hook",
    state: s.status === "succeeded" ? "done" : s.status === "running" ? "running" : s.status === "failed" ? "failed" : "skipped",
    meta: s.status === "skipped" ? "skipped" : s.started_at ? duration(s.started_at, s.finished_at ?? undefined) : "",
  });
  const items: TrackItem[] = [
    {
      key: "queued",
      phase: "plan",
      name: "Queued",
      state: r.started_at ? "done" : r.status === "canceled" ? "skipped" : "waiting",
      meta: `${r.created_by === "drift-detection" ? "schedule" : r.created_by} · ${timeAgo(r.created_at)}`,
    },
  ];
  const plan = steps.filter((s) => s.phase === "plan").map(fromStep);
  items.push(...(plan.length ? plan : placeholders("plan", ["Checkout", "Init", "Plan"], r.status === "canceled")));

  const applies = r.type === "apply" || r.type === "destroy";
  if (!applies) return items;
  const planFailed = r.status === "failed" && !r.approved_at;
  let approval: StepState = "pending";
  let meta = "";
  if (r.status === "awaiting_approval") {
    approval = "waiting";
    meta = `waiting ${duration(r.finished_at ?? r.updated_at)}`;
  } else if (r.approved_at) {
    approval = "done";
    meta = r.approved_by ? `by ${r.approved_by}` : "";
  } else if (r.status === "discarded") {
    approval = "skipped";
    meta = "discarded";
  } else if (r.status === "no_changes" || planFailed || r.status === "canceled") {
    approval = "skipped";
    meta = r.status === "no_changes" ? "nothing to apply" : "skipped";
  }
  items.push({ key: "approval", phase: "approval", name: "Approval", state: approval, meta });
  const apply = steps.filter((s) => s.phase === "apply").map(fromStep);
  items.push(...(apply.length ? apply : placeholders("apply", ["Checkout", "Init", "Apply"], approval === "skipped")));
  return items;
}

function placeholders(phase: "plan" | "apply", names: string[], skipped: boolean): TrackItem[] {
  return names.map((name) => ({ key: `${phase}/${name}`, phase, name, state: skipped ? "skipped" : "pending", meta: skipped ? "skipped" : "" }));
}

function defaultStep(track: TrackItem[]): string {
  const pick =
    track.find((t) => t.state === "running") ??
    track.find((t) => t.state === "failed") ??
    track.find((t) => t.state === "waiting") ??
    [...track].reverse().find((t) => t.state === "done");
  return (pick ?? track[0]).key;
}

const stateSymbol: Record<StepState, string> = { done: "✓", running: "", waiting: "!", pending: "", failed: "✕", skipped: "–" };

function StepTrack({ track, selected, onSelect }: { track: TrackItem[]; selected: string; onSelect: (k: string) => void }) {
  let lastPhase = "";
  return (
    <div className="track">
      {track.map((t, i) => {
        const showPhase = t.phase !== lastPhase;
        lastPhase = t.phase;
        const next = track[i + 1];
        return (
          <span key={t.key} className="track-item">
            {showPhase && <span className="track-phase">{t.phase}</span>}
            <button className={`step ${t.state}${selected === t.key ? " selected" : ""}`} aria-pressed={selected === t.key}
              aria-label={`${t.name}: ${t.state}. Show its log`} onClick={() => onSelect(t.key)}>
              <span className="step-name">
                <span className={`step-dot ${t.state}`} aria-hidden>
                  {stateSymbol[t.state]}
                </span>
                {t.name}
                {t.hook && <span className="hook-tag">hook</span>}
              </span>
              {t.meta && <span className="step-meta">{t.meta}</span>}
            </button>
            {next && next.phase === t.phase && <span className={`connector${t.state === "done" ? " done" : ""}`} aria-hidden />}
          </span>
        );
      })}
    </div>
  );
}

function StepLog({ track, selected, events, run, live, error }: {
  track: TrackItem[];
  selected: string;
  events: LogEvent[];
  run: Run;
  live: boolean;
  error: string | null;
}) {
  const item = track.find((t) => t.key === selected) ?? track[0];
  const ref = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const text = useMemo(() => {
    if (item.key === "queued") {
      return run.started_at ? `Queued by ${run.created_by} at ${new Date(run.created_at).toLocaleTimeString()}.\nPicked up by an agent.` : "Waiting for an agent to pick up this run.";
    }
    if (item.key === "approval") {
      if (run.status === "awaiting_approval") return "Waiting for approval from an organization administrator.";
      if (run.approved_by) return `Approved by ${run.approved_by}${run.acknowledged?.length ? `, confirming: ${run.acknowledged.join(", ")}` : ""}.`;
      return item.state === "skipped" ? "No approval needed." : "";
    }
    const [phase, name] = item.key.split("/");
    return events.filter((e) => e.phase === phase && e.step === name).map((e) => e.data).join("");
  }, [item, events, run]);
  const lines = text.replace(/\n$/, "").split("\n");

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow) el.scrollTop = el.scrollHeight;
  }, [lines.length, follow, selected]);

  return (
    <div className="log-panel">
      <div className="log-head">
        <span className="log-title">Log · {item.name}</span>
        <span className="log-meta">{item.meta}</span>
        {live && item.state === "running" && <span className="dot pulse live" aria-label="streaming" />}
        {error && <span className="log-error-msg">{error}</span>}
      </div>
      <div className="log-view" ref={ref} onScroll={() => {
        const el = ref.current;
        if (el) setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
      }}>
        {text === "" ? (
          <div className="log-line dim">{item.state === "pending" ? "Not started yet." : item.state === "skipped" ? "Skipped." : "No output yet."}</div>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={`log-line${l.startsWith("==>") ? " log-step" : ""}${/^(Error|│ Error|==> Error)/.test(l) ? " log-error" : ""}`}>
              {l || " "}
            </div>
          ))
        )}
      </div>
    </div>
  );
}

/**
 * Streams the run's logs. The server ends the stream when the run finishes or
 * stops for approval; it resumes from the last event once the run moves on.
 */
function useLogs(url: string, run: Run | null, reloadRun: () => Promise<void>) {
  const [events, setEvents] = useState<LogEvent[]>([]);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);
  const cursor = useRef("");

  useEffect(() => {
    cursor.current = "";
    setEvents([]);
  }, [url]);

  useEffect(() => {
    const ctrl = new AbortController();
    setStreaming(true);
    streamLogs(url, cursor.current, { onLog: (e) => setEvents((xs) => [...xs, e]), onEnd: () => {} }, ctrl.signal)
      .then((c) => {
        cursor.current = c;
        setError(null);
      })
      .catch(async (e) => {
        if (ctrl.signal.aborted) return;
        setError(String(e?.message ?? e));
        await new Promise((res) => setTimeout(res, 3000));
      })
      .finally(() => {
        if (ctrl.signal.aborted) return;
        setStreaming(false);
        void reloadRun();
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, generation]);

  const status = run?.status;
  useEffect(() => {
    if (!streaming && status && !isTerminal(status) && status !== "awaiting_approval") setGeneration((g) => g + 1);
  }, [streaming, status]);

  return { events, streaming, error };
}

// --- changes ---

const actionOrder: ChangeAction[] = ["delete", "replace", "update", "create"];
const actionInfo: Record<ChangeAction, { label: string; group: string; driftGroup: string; tone: string }> = {
  delete: { label: "Delete", group: "Will be deleted", driftGroup: "Exists only in the real infrastructure", tone: "bad" },
  replace: { label: "Replace", group: "Will be replaced (deleted and created again)", driftGroup: "Would be replaced to match the code", tone: "rep" },
  update: { label: "Update", group: "Will be updated in place", driftGroup: "Changed outside Firstlume", tone: "warn" },
  create: { label: "Create", group: "Will be created", driftGroup: "Missing from the real infrastructure", tone: "ok" },
};

function ActionCounts({ changes }: { changes: ResourceChange[] }) {
  const count = (a: ChangeAction) => changes.filter((c) => c.action === a).length;
  return (
    <span className="action-counts">
      {actionOrder.map((a) => (
        <span key={a} className={`badge ${actionInfo[a].tone}${count(a) ? "" : " zero"}`}>
          {count(a)} {a}
        </span>
      ))}
    </span>
  );
}

function ChangeList({ changes, drift, ack, planning }: {
  changes: ResourceChange[] | null;
  drift: boolean;
  ack: { acked: Record<string, boolean>; toggle: (k: string) => void; origin?: Level } | null;
  planning: boolean;
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({});
  if (!changes) return null;
  if (changes.length === 0) {
    return <div className="empty">{planning ? "The plan is still running. Changes show up here when it finishes." : "Nothing changes."}</div>;
  }
  const allOpen = changes.every((c) => open[changeKey(c)]);
  return (
    <>
      <div className="change-toolbar">
        <button className="link" onClick={() => setOpen(allOpen ? {} : Object.fromEntries(changes.map((c) => [changeKey(c), true])))}>
          {allOpen ? "Collapse all" : "Expand all"}
        </button>
      </div>
      {actionOrder.map((a) => {
        const items = changes.filter((c) => c.action === a);
        if (!items.length) return null;
        return (
          <div key={a} className="change-group">
            <h3 className={`group-title tone-${actionInfo[a].tone}`}>
              {drift ? actionInfo[a].driftGroup : actionInfo[a].group} · {items.length}
            </h3>
            {items.map((c) => (
              <ChangeCard
                key={changeKey(c)}
                change={c}
                drift={drift}
                ack={ack}
                open={!!open[changeKey(c)]}
                onToggle={() => setOpen((o) => ({ ...o, [changeKey(c)]: !o[changeKey(c)] }))}
              />
            ))}
          </div>
        );
      })}
    </>
  );
}

// Collapsed by default: a long plan reads as a list of names, and the
// attribute diff opens per resource. Warnings and acks always show.
function ChangeCard({ change: c, drift, ack, open, onToggle }: {
  change: ResourceChange;
  drift: boolean;
  ack: { acked: Record<string, boolean>; toggle: (k: string) => void; origin?: Level } | null;
  open: boolean;
  onToggle: () => void;
}) {
  const info = actionInfo[c.action];
  const key = changeKey(c);
  const destructive = c.action === "delete" || c.action === "replace";
  const forcing = (c.attributes ?? []).filter((a) => a.forces_replacement).map((a) => a.path);
  const nattrs = c.attributes?.length ?? 0;
  return (
    <article className={`change-card edge-${info.tone}`}>
      <button className="change-head" aria-expanded={open} onClick={onToggle} disabled={!nattrs}>
        <span className={`change-chev${open ? " open" : ""}`} aria-hidden>
          {nattrs > 0 && <ChevronRightIcon />}
        </span>
        <ResourceIcon type={c.type} size={28} />
        <span className={`badge ${info.tone}`}>{info.label}</span>
        {c.label ? <LabelLine label={c.label} data={false} /> : <strong>{c.name}</strong>}
        <span className="muted small">{humanType(c.type)}</span>
        {!open && nattrs > 0 && (
          <span className="muted small">
            · {nattrs}
            {c.truncated ? "+" : ""} attribute{nattrs === 1 ? "" : "s"}
          </span>
        )}
        <span className="grow" />
        <span className="mono muted small">{c.unit ? `${c.unit} · ${c.address}` : c.address}</span>
      </button>
      {c.action === "delete" && !drift && <p className="change-warning bad">This resource and anything stored in it will be deleted.</p>}
      {c.action === "replace" && (
        <p className="change-warning rep">
          {forcing.length ? <>Changing <span className="mono">{forcing.join(", ")}</span> forces Terraform to delete this resource and create a new one.</> : "Terraform will delete this resource and create a new one."}{" "}
          Anything that depends on it may be affected.
        </p>
      )}
      {open && nattrs > 0 && (
        <div className="diff">
          {drift && <><span className="diff-head">attribute</span><span className="diff-head">real infrastructure → code</span></>}
          {c.attributes!.map((a) => (
            <span key={a.path} className="diff-row">
              <span className="mono muted">{a.path}</span>
              <span className="mono diff-values">
                {c.action === "create" ? (
                  <span className="after">{a.after}</span>
                ) : (
                  <>
                    <span className="before">{a.before ?? "(none)"}</span> <span className="muted">→</span> <span className="after">{a.after ?? "(none)"}</span>
                  </>
                )}
                {a.forces_replacement && <span className="forces">forces replacement</span>}
              </span>
            </span>
          ))}
          {c.truncated && <span className="muted small diff-more">More attributes change than are listed.</span>}
        </div>
      )}
      {ack && destructive && (
        <label className={`ack${ack.acked[key] ? " done" : ""}`}>
          <input type="checkbox" checked={!!ack.acked[key]} onChange={() => ack.toggle(key)} />
          <span className="grow">
            I understand <strong>{c.label?.name ?? c.name}</strong> will be {c.action === "delete" ? "deleted" : "deleted and created again"}
          </span>
          {ack.origin && <span className="muted small">required by {levelName(ack.origin)}</span>}
        </label>
      )}
    </article>
  );
}

// --- side cards ---

function CommitLine({ run, stack }: { run: Run; stack: Stack | null }) {
  const url = stack ? commitURL(stack.repository, run.commit_sha) : null;
  return (
    <span className="muted small">
      {run.commit_author_name && `${run.commit_author_name} · `}
      {run.commit_message && `“${run.commit_message}” · `}
      {run.commit_sha ? (
        url ? (
          <a className="mono" href={url} target="_blank" rel="noreferrer">
            {run.commit_sha.slice(0, 7)} <ExternalIcon size={11} />
          </a>
        ) : (
          <span className="mono">{run.commit_sha.slice(0, 7)}</span>
        )
      ) : (
        <span className="mono">{run.ref}</span>
      )}
    </span>
  );
}

function CommitCard({ run, stack }: { run: Run; stack: Stack | null }) {
  const url = stack ? commitURL(stack.repository, run.commit_sha) : null;
  return (
    <section className="card side-card">
      <h2 className="card-label">Commit</h2>
      {run.commit_sha ? (
        <>
          <div className="commit-who">
            <Initials name={run.commit_author_name} size={32} />
            <span>
              <strong>{run.commit_message || "(no message)"}</strong>
              <span className="muted small">
                {run.commit_author_name}
                {run.commit_author_email && ` · ${run.commit_author_email}`}
              </span>
            </span>
          </div>
          <div className="mono small">
            {run.ref} · {run.commit_sha.slice(0, 7)}
          </div>
          {url && (
            <a className="small-btn fit" href={url} target="_blank" rel="noreferrer">
              <ExternalIcon /> View commit in Git
            </a>
          )}
        </>
      ) : (
        <p className="muted small">Resolved when the plan checks out {run.ref}.</p>
      )}
    </section>
  );
}

function ConfigCard({ run, stack }: { run: Run; stack: Stack }) {
  const { link } = useOrg();
  const used = run.settings_used;
  const cfgPath = `${stack.path === "." ? "" : stack.path + "/"}.firstlume/config.yaml`;
  const gitLink = fileURL(stack.repository, run.commit_sha, cfgPath);
  const editLink = (o: Level) =>
    o.kind === "folder" ? link(`f/${o.id}/settings`) : o.kind === "stack" ? link(`s/${o.id}/settings`) : link("settings");
  const editLabel = (o: Level) => (o.kind === "default" ? "Set in organization" : `Edit in ${levelName(o)}`);
  const rows = used
    ? [
        { label: "Approval required", value: used.require_approval.value ? "Yes" : "No", origin: used.require_approval.origin },
        { label: "Confirm deletes and replaces", value: used.confirm_destructive.value ? "Yes" : "No", origin: used.confirm_destructive.origin },
        {
          label: "Drift check",
          value: used.drift_interval_minutes.value ? `Every ${used.drift_interval_minutes.value} min` : "Off",
          origin: used.drift_interval_minutes.origin,
        },
      ]
    : [];

  return (
    <section className="card side-card config-card" aria-label="Configuration">
      <h2 className="card-label">Configuration</h2>
      <details>
        <summary>
          config.yaml {run.commit_sha && <span className="mono muted">· {run.commit_sha.slice(0, 7)}</span>}
        </summary>
        {run.config ? (
          <>
            <div className="mono muted small wrap">{cfgPath}</div>
            <pre className="code-block">
              {`version: ${run.config.version}\nengine: ${run.config.engine}\nengine_version: ${run.config.engine_version}` +
                (run.config.terragrunt_version ? `\nterragrunt_version: ${run.config.terragrunt_version}` : "") +
                (run.config.run_all ? "\nrun_all: true" : "")}
            </pre>
            {gitLink && (
              <a href={gitLink} target="_blank" rel="noreferrer" className="small">
                Open in Git <ExternalIcon size={11} />
              </a>
            )}
          </>
        ) : (
          <p className="muted small">Not read yet.</p>
        )}
      </details>
      <details>
        <summary>
          Settings used by this run <span className="muted">· {rows.length}</span>
        </summary>
        {used ? (
          <ul className="used-list">
            {rows.map((row) => (
              <li key={row.label}>
                <span className="grow">
                  <span className="muted small">{row.label}</span>
                  <strong>{row.value}</strong>
                </span>
                <Link className="small-btn" to={editLink(row.origin)}>
                  {editLabel(row.origin)}
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted small">Recorded when the plan starts.</p>
        )}
      </details>
      <details>
        <summary>
          Variables <span className="muted">· {used?.variables.length ?? 0}</span>
        </summary>
        <ul className="used-list">
          {(used?.variables ?? []).map((v) => (
            <li key={`${v.category}/${v.key}`}>
              <span className="grow">
                <span className="mono">{v.key}</span>
                <span className="mono muted small wrap">{v.sensitive ? "••••••" : v.value}</span>
              </span>
              <Link className="small-btn" to={editLink(v.origin)}>
                {editLabel(v.origin)}
              </Link>
            </li>
          ))}
        </ul>
      </details>
    </section>
  );
}
