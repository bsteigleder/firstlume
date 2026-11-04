import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import {
  changeKey,
  isTerminal,
  type ApplyError,
  type ApplyState,
  type ApplyStep,
  settingsPath,
  streamLogs,
  type ChangeAction,
  type Level,
  type LogEvent,
  type ResourceChange,
  type Run,
  type RunStep,
  type Stack,
  type UnitBackend,
} from "../api";
import { CheckIcon, ChevronRightIcon, CloseIcon, ExternalIcon } from "../icons";
import { useCrumbs, useOrg } from "../org";
import {
  commitURL,
  duration,
  ErrorBanner,
  fileURL,
  humanType,
  Initials,
  levelName,
  runOriginText,
  StatusBadge,
  supersededBy,
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
  const [filter, setFilter] = useState<StateFilter>("all");
  const [logOpen, setLogOpen] = useStoredFlag("firstlume.applyLogOpen");
  const [focus, setFocus] = useState<LogFocus | null>(null);

  // Keep the run, its steps and, during the apply, its changes fresh while they can still change.
  useEffect(() => {
    if (!active) return;
    const refresh = () => {
      void run.reload();
      void steps.reload();
      if (status === "applying") void changes.reload();
    };
    const t = setInterval(refresh, 2000);
    const stop = onTabShown(refresh);
    return () => {
      clearInterval(t);
      stop();
    };
  }, [active, status, run.reload, steps.reload, changes.reload]);
  const counts = applyCounts(changes.data);
  const now = useNow(!!counts && counts.applying > 0);
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
  const track = buildTrack(r, steps.data ?? [], counts);
  const current = selected ?? defaultStep(track);
  const confirmRequired = settings.data?.effective.confirm_destructive.value ?? false;
  const destructive = (changes.data ?? []).filter((c) => c.action === "delete" || c.action === "replace");
  const missing = confirmRequired ? destructive.filter((c) => !acked[changeKey(c)]) : [];
  const awaiting = r.status === "awaiting_approval";
  const isDrift = r.type === "drift";

  const applyStep = (steps.data ?? []).find((st) => st.phase === "apply" && st.name === "Apply");
  const applyDuration = applyStep?.started_at && applyStep.finished_at ? duration(applyStep.started_at, applyStep.finished_at) : "";
  const failedChanges = (changes.data ?? []).filter((c) => c.apply?.state === "failed");
  // Resources that failed or never ran explain the failure better than the run's error (R4.9).
  const explained = !!counts && counts.failed + counts.not_applied > 0;
  const apply: ApplyView | null = counts
    ? {
        filter,
        now,
        showInLog: (c) => {
          setSelected(APPLY_STEP);
          setLogOpen(true);
          setFocus({ address: c.address, unit: c.unit, seq: Date.now() });
        },
        fileLink: (c, e) => {
          if (!s || !r.commit_sha || !e.file || e.file.startsWith("/") || e.file.split("/").includes("..") || e.file.includes(".terragrunt-cache")) return null;
          const path = [s.path, c.unit, e.file].filter((p) => p && p !== ".").join("/");
          const url = fileURL(s.repository, r.commit_sha, path);
          return url && e.line ? `${url}#L${e.line}` : url;
        },
      }
    : null;
  const summaryText = counts
    ? active
      ? `${counts.done + counts.failed} of ${counts.total} finished${r.approved_by ? ` · approved by ${r.approved_by}` : ""}`
      : `${counts.done} of ${counts.total} applied${applyDuration ? ` · finished in ${applyDuration}` : ""}`
    : "";

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
      {r.trigger?.kind === "push" && <TriggerLine run={r} />}
      <ErrorBanner error={action.error} onDismiss={() => action.setError(null)} />
      {supersededBy(r) && <SupersededNotice run={r} />}
      {r.status === "queued" && r.trigger?.kind === "push" && <WaitingForStack run={r} />}

      {counts && active && counts.failed > 0 && (
        <div className="error-banner" role="alert">
          <span>
            <strong>{failedChanges.length === 1 ? `${failedChanges[0].name} failed.` : `${plural(counts.failed, "change")} failed.`}</strong> Changes already
            running will finish; what depends on {counts.failed === 1 ? "it" : "them"} won’t start.
          </span>
          <button type="button" className="small-btn" onClick={() => setFilter("failed")}>
            Show failed
          </button>
        </div>
      )}
      {counts && r.status === "failed" && explained && (
        <div className="error-banner" role="alert">
          <span>
            <strong>Apply failed.</strong> {counts.done} of {plural(counts.total, "change")} {counts.done === 1 ? "was" : "were"} applied, {counts.failed} failed and{" "}
            {counts.not_applied} {counts.not_applied === 1 ? "was" : "were"} not applied. Fix the error and run again: the next plan only has what is left.
          </span>
          {counts.failed > 0 && (
            <button type="button" className="small-btn" onClick={() => setFilter("failed")}>
              Show failed
            </button>
          )}
        </div>
      )}
      {counts && r.status === "applied" && (
        <div className="banner banner-ok" role="status">
          <span className="banner-text">
            <strong>All {plural(counts.total, "change")} applied.</strong>
            {applyDuration && ` Finished in ${applyDuration}.`}
          </span>
        </div>
      )}
      {r.status === "failed" && r.error && !explained && (
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
        <StepLog track={track} selected={current} events={logs.events} run={r} live={logs.streaming} error={logs.error}
          collapse={counts && current === APPLY_STEP ? { open: logOpen, toggle: () => setLogOpen(!logOpen) } : null} focus={focus} />
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
          {counts ? (
            <ApplySummary counts={counts} text={summaryText} filter={filter} onFilter={setFilter} />
          ) : (
            <h2>{isDrift ? "What differs from the code" : "What changes"}</h2>
          )}
          <ChangeList
            changes={changes.data}
            drift={isDrift}
            ack={awaiting && confirmRequired ? { acked, toggle: (k) => setAcked((a) => ({ ...a, [k]: !a[k] })), origin: settings.data?.effective.confirm_destructive.origin } : null}
            planning={r.status === "queued" || r.status === "planning"}
            backends={r.unit_backends}
            apply={apply}
          />
        </section>
        <aside className="run-aside">
          <CommitCard run={r} stack={s} />
          <StateCard run={r} />
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

const APPLY_STEP = "apply/Apply";

/** The run's steps: the executor's, plus "Queued" and "Approval", which the server knows. */
function buildTrack(r: Run, steps: RunStep[], counts: ApplyCounts | null): TrackItem[] {
  const fromStep = (s: RunStep): TrackItem => {
    let meta = s.status === "skipped" ? "skipped" : s.started_at ? duration(s.started_at, s.finished_at ?? undefined) : "";
    // The running apply also says how far it got (R4.10).
    if (counts && s.status === "running" && `${s.phase}/${s.name}` === APPLY_STEP) {
      meta += ` · ${counts.done + counts.failed} of ${counts.total}`;
    }
    return {
      key: `${s.phase}/${s.name}`,
      phase: s.phase,
      name: s.name,
      hook: s.kind === "hook",
      state: s.status === "succeeded" ? "done" : s.status === "running" ? "running" : s.status === "failed" ? "failed" : "skipped",
      meta,
    };
  };
  const items: TrackItem[] = [
    {
      key: "queued",
      phase: "plan",
      name: "Queued",
      state: r.started_at ? "done" : r.status === "canceled" ? "skipped" : "waiting",
      meta: `${runOriginText(r)} · ${timeAgo(r.created_at)}`,
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

/** Where "Show in log" points: the error block of a resource. */
interface LogFocus {
  address: string;
  unit?: string;
  seq: number;
}

function StepLog({ track, selected, events, run, live, error, collapse, focus }: {
  track: TrackItem[];
  selected: string;
  events: LogEvent[];
  run: Run;
  live: boolean;
  error: string | null;
  /** While the apply reports progress, its log shows only the last lines (R4.8). */
  collapse: { open: boolean; toggle: () => void } | null;
  focus: LogFocus | null;
}) {
  const item = track.find((t) => t.key === selected) ?? track[0];
  const ref = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const text = useMemo(() => {
    if (item.key === "queued") {
      return run.started_at ? `Queued by ${runOriginText(run)} at ${new Date(run.created_at).toLocaleTimeString()}.\nPicked up by an agent.` : "Waiting for an agent to pick up this run.";
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
  const collapsed = !!collapse && !collapse.open && lines.length > 4;
  const shown = collapsed ? lines.slice(-3) : lines;
  const offset = lines.length - shown.length;

  // The first line of the focused resource's error block: "with <address>,".
  const focusLine = useMemo(() => {
    if (!focus) return -1;
    return lines.findIndex((l) => l.includes(`with ${focus.address},`) && (!focus.unit || l.startsWith(`[${focus.unit}] `)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, text]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow && !focus) el.scrollTop = el.scrollHeight;
  }, [lines.length, follow, selected, focus]);

  useEffect(() => {
    const el = ref.current;
    if (!el || focusLine < 0 || collapsed) return;
    const line = el.querySelector<HTMLElement>(`[data-line="${focusLine}"]`);
    if (!line) return;
    setFollow(false);
    el.scrollTop = Math.max(0, line.offsetTop - el.offsetTop - 60);
    el.closest(".log-panel")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    // Once per click.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus?.seq, focusLine, collapsed]);

  return (
    <div className="log-panel">
      <div className="log-head">
        <span className="log-title">Log · {item.name}</span>
        <span className="log-meta">{item.meta}</span>
        {live && item.state === "running" && <span className="dot pulse live" aria-label="streaming" />}
        {error && <span className="log-error-msg">{error}</span>}
        {collapse && lines.length > 4 && (
          <>
            <span className="grow" />
            <button type="button" className="small-btn log-toggle" aria-expanded={collapse.open} onClick={collapse.toggle}>
              {collapse.open ? "Collapse log" : "Expand log"}
            </button>
          </>
        )}
      </div>
      <div className="log-view" ref={ref} onScroll={() => {
        const el = ref.current;
        if (el) setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
      }}>
        {text === "" ? (
          <div className="log-line dim">{item.state === "pending" ? "Not started yet." : item.state === "skipped" ? "Skipped." : "No output yet."}</div>
        ) : (
          <>
            {collapsed && <div className="log-line dim">… {offset} earlier lines</div>}
            {shown.map((l, i) => {
              const n = i + offset;
              const err = /^(\[[^\]]+\] )?(Error|│ Error|==> Error)/.test(l);
              return (
                <div key={n} data-line={n}
                  className={`log-line${l.startsWith("==>") ? " log-step" : ""}${err ? " log-error" : ""}${focus && n >= focusLine && focusLine >= 0 && n < focusLine + 4 ? " log-focus" : ""}`}>
                  {l || " "}
                </div>
              );
            })}
          </>
        )}
      </div>
    </div>
  );
}

/** A flag kept in the browser, for per-viewer preferences. */
function useStoredFlag(key: string): [boolean, (v: boolean) => void] {
  const [value, setValue] = useState(() => {
    try {
      return localStorage.getItem(key) === "1";
    } catch {
      return false;
    }
  });
  const set = useCallback(
    (v: boolean) => {
      setValue(v);
      try {
        localStorage.setItem(key, v ? "1" : "0");
      } catch {
        // Storage unavailable: the choice lasts until the page is left.
      }
    },
    [key],
  );
  return [value, set];
}

/** The current time, ticking every second while enabled (for elapsed times). */
function useNow(enabled: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [enabled]);
  return now;
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
const actionInfo: Record<ChangeAction, { label: string; group: string; driftGroup: string; applyGroup: string; applied: string; tone: string }> = {
  delete: { label: "Delete", group: "Will be deleted", driftGroup: "Exists only in the real infrastructure", applyGroup: "Delete", applied: "Deleted", tone: "bad" },
  replace: { label: "Replace", group: "Will be replaced (deleted and created again)", driftGroup: "Would be replaced to match the code", applyGroup: "Replace", applied: "Replaced", tone: "rep" },
  update: { label: "Update", group: "Will be updated in place", driftGroup: "Changed outside First Lume", applyGroup: "Update in place", applied: "Updated", tone: "warn" },
  create: { label: "Create", group: "Will be created", driftGroup: "Missing from the real infrastructure", applyGroup: "Create", applied: "Created", tone: "ok" },
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

// --- apply progress (spec 0004) ---

type StateFilter = "all" | ApplyState;

/** Failed first: what needs attention leads each group (R4.6). */
const stateOrder: ApplyState[] = ["failed", "applying", "not_applied", "pending", "done"];
const stateInfo: Record<ApplyState, { chip: string; color: string }> = {
  failed: { chip: "Failed", color: "var(--danger-strong)" },
  applying: { chip: "Applying", color: "var(--running)" },
  not_applied: { chip: "Not applied", color: "var(--border-strong)" },
  pending: { chip: "Waiting", color: "var(--surface)" },
  done: { chip: "Done", color: "var(--success)" },
};

type ApplyCounts = Record<ApplyState, number> & { total: number };

/** Counts resources by state, or null when the apply does not report progress. */
function applyCounts(changes: ResourceChange[] | null): ApplyCounts | null {
  if (!changes?.some((c) => c.apply)) return null;
  const counts: ApplyCounts = { total: changes.length, pending: 0, applying: 0, done: 0, failed: 0, not_applied: 0 };
  for (const c of changes) counts[c.apply?.state ?? "pending"]++;
  return counts;
}

function msLabel(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`;
}

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

/** What a change card needs to show the apply. */
interface ApplyView {
  filter: StateFilter;
  now: number;
  showInLog: (c: ResourceChange) => void;
  fileLink: (c: ResourceChange, e: ApplyError) => string | null;
}

function ApplySummary({ counts, text, filter, onFilter }: { counts: ApplyCounts; text: string; filter: StateFilter; onFilter: (f: StateFilter) => void }) {
  const label = `${counts.done} done, ${counts.failed} failed, ${counts.applying} applying, ${counts.not_applied} not applied, ${counts.pending} waiting`;
  const chips: StateFilter[] = ["all", ...stateOrder.filter((st) => counts[st] > 0)];
  return (
    <div className="apply-summary">
      <div className="apply-summary-head">
        <h2>What changes</h2>
        <span className="muted small">{text}</span>
      </div>
      <div className="apply-bar" role="img" aria-label={label}>
        {(["done", "failed", "applying", "not_applied"] as ApplyState[]).map((st) =>
          counts[st] ? <span key={st} style={{ flex: counts[st], background: stateInfo[st].color }} /> : null,
        )}
        {counts.pending > 0 && <span style={{ flex: counts.pending }} />}
      </div>
      <div className="filter-chips" role="group" aria-label="Filter by status">
        {chips.map((f) => (
          <button key={f} type="button" className={`filter-chip${filter === f ? " on" : ""}`} aria-pressed={filter === f} onClick={() => onFilter(f)}>
            {f !== "all" && <i className="swatch" style={{ background: stateInfo[f].color }} aria-hidden />}
            {f === "all" ? "All" : stateInfo[f].chip} · {f === "all" ? counts.total : counts[f]}
          </button>
        ))}
      </div>
    </div>
  );
}

function ChangeList({ changes, drift, ack, planning, backends, apply }: {
  changes: ResourceChange[] | null;
  drift: boolean;
  ack: { acked: Record<string, boolean>; toggle: (k: string) => void; origin?: Level } | null;
  planning: boolean;
  backends?: UnitBackend[];
  apply: ApplyView | null;
}) {
  const [open, setOpen] = useState<Record<string, boolean>>({});
  // Units outside Firstlume get a chip on their cards; the normal case gets none.
  const elsewhere = new Map((backends ?? []).filter((b) => b.backend !== "firstlume").map((b) => [b.unit, b.backend]));
  if (!changes) return null;
  if (changes.length === 0) {
    return <div className="empty">{planning ? "The plan is still running. Changes show up here when it finishes." : "Nothing changes."}</div>;
  }
  const rank = (c: ResourceChange) => stateOrder.indexOf(c.apply?.state ?? "pending");
  const groups = actionOrder
    .map((a) => {
      const all = changes.filter((c) => c.action === a);
      if (!apply) return { action: a, all, items: all };
      const items = all.filter((c) => apply.filter === "all" || (c.apply?.state ?? "pending") === apply.filter);
      return { action: a, all, items: [...items].sort((x, y) => rank(x) - rank(y)) };
    })
    .filter((g) => g.items.length > 0);
  if (groups.length === 0) return <div className="empty">No changes in this state.</div>;
  // Collapsed by default; during an apply, what needs attention starts open (R4.7).
  const startsOpen = (c: ResourceChange) => !!apply && ["failed", "applying", "not_applied"].includes(c.apply?.state ?? "");
  const isOpen = (c: ResourceChange) => open[changeKey(c)] ?? startsOpen(c);
  const allOpen = changes.every(isOpen);
  return (
    <>
      <div className="change-toolbar">
        <button className="link" onClick={() => setOpen(Object.fromEntries(changes.map((c) => [changeKey(c), !allOpen])))}>
          {allOpen ? "Collapse all" : "Expand all"}
        </button>
      </div>
      {groups.map(({ action: a, all, items }) => {
        const info = actionInfo[a];
        const title = apply ? info.applyGroup : drift ? info.driftGroup : info.group;
        const count = apply && apply.filter !== "all" ? `${items.length} of ${all.length}` : items.length;
        return (
          <div key={a} className="change-group">
            <h3 className={`group-title tone-${info.tone}`}>
              {title} · {count}
            </h3>
            {items.map((c) => (
              <ChangeCard
                key={changeKey(c)}
                change={c}
                drift={drift}
                ack={ack}
                apply={apply}
                backend={elsewhere.get(c.unit ?? "")}
                open={isOpen(c)}
                onToggle={() => setOpen((o) => ({ ...o, [changeKey(c)]: !isOpen(c) }))}
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
function ChangeCard({ change: c, drift, ack, apply, backend, open, onToggle }: {
  change: ResourceChange;
  drift: boolean;
  ack: { acked: Record<string, boolean>; toggle: (k: string) => void; origin?: Level } | null;
  apply: ApplyView | null;
  // backend is where the card's unit keeps its state, when not in Firstlume.
  backend?: string;
  open: boolean;
  onToggle: () => void;
}) {
  const info = actionInfo[c.action];
  const key = changeKey(c);
  const destructive = c.action === "delete" || c.action === "replace";
  const forcing = (c.attributes ?? []).filter((a) => a.forces_replacement).map((a) => a.path);
  const nattrs = c.attributes?.length ?? 0;
  const progress = apply ? c.apply : undefined;
  const opens = nattrs > 0 || (c.action === "replace" && (progress?.steps?.length ?? 0) > 0);
  const address = c.unit ? `${c.unit} · ${c.address}` : c.address;
  return (
    <article className={`change-card edge-${info.tone}${progress ? ` apply-${progress.state}` : ""}`}>
      <button className="change-head" aria-expanded={open} onClick={onToggle} disabled={!opens}>
        <span className={`change-chev${open ? " open" : ""}`} aria-hidden>
          {opens && <ChevronRightIcon />}
        </span>
        <ResourceIcon type={c.type} size={28} />
        <span className={`badge ${info.tone}`}>{info.label}</span>
        {c.label ? <LabelLine label={c.label} data={false} /> : <strong>{c.name}</strong>}
        <span className="muted small">{humanType(c.type)}</span>
        {!open && !progress && nattrs > 0 && (
          <span className="muted small">
            · {nattrs}
            {c.truncated ? "+" : ""} attribute{nattrs === 1 ? "" : "s"}
          </span>
        )}
        {progress && <span className="mono muted small">{address}</span>}
        <span className="grow" />
        {backend && (
          <span className={backend === "local" ? "badge bad" : "badge idle"} title={`This unit keeps its state in backend ${backend}`}>
            state: {backend}
          </span>
        )}
        {progress ? <StateChip change={c} now={apply!.now} /> : <span className="mono muted small">{address}</span>}
      </button>
      {progress?.state === "failed" && (progress.errors?.length ?? 0) > 0 && (
        <ResourceErrors errors={progress.errors!} link={(e) => apply!.fileLink(c, e)} onShowInLog={() => apply!.showInLog(c)} />
      )}
      {progress?.state === "not_applied" && <p className="small muted">Not applied: the apply stopped before reaching it. It will be in the next plan.</p>}
      {!progress && c.action === "delete" && !drift && <p className="change-warning bad">This resource and anything stored in it will be deleted.</p>}
      {!progress && c.action === "replace" && (
        <p className="change-warning rep">
          {forcing.length ? <>Changing <span className="mono">{forcing.join(", ")}</span> forces Terraform to delete this resource and create a new one.</> : "Terraform will delete this resource and create a new one."}{" "}
          Anything that depends on it may be affected.
        </p>
      )}
      {open && progress && c.action === "replace" && (progress.steps?.length ?? 0) > 0 && <ReplaceSteps steps={progress.steps!} />}
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
                {a.forces_replacement && <span className="forces">{progress ? "forced replacement" : "forces replacement"}</span>}
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

// --- started by a trigger (spec 0006, canvas board 59) ---

function TriggerLine({ run }: { run: Run }) {
  const { link } = useOrg();
  const t = run.trigger!;
  const who = t.actor ?? run.created_by;
  return (
    <p className="muted small trigger-line">
      {t.pull_request ? (
        <>
          <strong>Started by the merge</strong> of{" "}
          {t.pull_request_url ? (
            <a href={t.pull_request_url} target="_blank" rel="noreferrer">
              pull request #{t.pull_request} <ExternalIcon size={11} />
            </a>
          ) : (
            `pull request #${t.pull_request}`
          )}
          , by {who}.
        </>
      ) : (
        <>
          <strong>Started by a push</strong> from {who}.
        </>
      )}{" "}
      {t.event_id && <Link to={link(`git-events?event=${t.event_id}`)}>See the event</Link>}
    </p>
  );
}

function SupersededNotice({ run }: { run: Run }) {
  const { api, link } = useOrg();
  const sha = supersededBy(run) ?? "";
  const runs = useData(() => api.listRuns(run.stack_id), [api, run.stack_id]);
  const newer = runs.data?.find((r) => r.commit_sha?.startsWith(sha));
  return (
    <p className="notice">
      <strong>A newer push reached <span className="mono">{run.ref}</span>{run.status === "canceled" ? " before this run started" : ""}.</strong>{" "}
      {run.commit_sha && (
        <>
          This run was for <span className="mono">{run.commit_sha.slice(0, 7)}</span>, and it was replaced by <span className="mono">{sha.slice(0, 7)}</span>.{" "}
        </>
      )}
      {newer && <Link to={link(`r/${newer.id}`)}>Open the new run</Link>}
    </p>
  );
}

function WaitingForStack({ run }: { run: Run }) {
  const { api, link } = useOrg();
  const runs = useData(() => api.listRuns(run.stack_id), [api, run.stack_id], { poll: true, pollMs: 4000 });
  const holding = runs.data?.find((r) => r.id !== run.id && ["planning", "awaiting_approval", "apply_queued", "applying"].includes(r.status));
  if (!holding) return null;
  return (
    <p className="notice">
      <strong>
        Waiting for run <Link className="mono" to={link(`r/${holding.id}`)}>{holding.id.slice(0, 8)}</Link>
      </strong>
      , which is using this stack.
    </p>
  );
}

// --- apply progress: what a card shows (spec 0004) ---

function StateChip({ change: c, now }: { change: ResourceChange; now: number }) {
  const a = c.apply!;
  switch (a.state) {
    case "done":
      return (
        <span className="state-chip done">
          <CheckIcon /> {actionInfo[c.action].applied} {a.started_at && <span className="t">· {msLabel(a.duration_ms ?? 0)}</span>}
        </span>
      );
    case "applying": {
      const running = [...(a.steps ?? [])].reverse().find((st) => !st.finished_at);
      const verb = running?.action === "delete" ? "Deleting…" : running?.action === "update" ? "Updating…" : "Creating…";
      return (
        <span className="state-chip run">
          <span className="spin" aria-hidden /> {verb} {a.started_at && <span className="t">{msLabel(now - Date.parse(a.started_at))}</span>}
        </span>
      );
    }
    case "failed":
      return (
        <span className="state-chip fail">
          <CloseIcon size={13} /> Failed {a.duration_ms ? <span className="t">· after {msLabel(a.duration_ms)}</span> : null}
        </span>
      );
    case "not_applied":
      return <span className="state-chip skip">Not applied</span>;
    default:
      return <span className="state-chip wait">Waiting</span>;
  }
}

/** The two halves of a replace, in the order they ran (R4.2). */
function ReplaceSteps({ steps }: { steps: ApplyStep[] }) {
  const label = (st: ApplyStep) => {
    const old = st.action === "delete";
    if (st.failed) return old ? "Deleting the old one failed" : "Creating the new one failed";
    if (!st.finished_at) return old ? "Deleting the old one…" : "Creating the new one…";
    return old ? "Old one deleted" : "New one created";
  };
  return (
    <div className="substeps">
      {steps.map((st, i) => (
        <Fragment key={i}>
          {i > 0 && <span className="muted" aria-hidden>→</span>}
          <span className={`state-chip ${st.failed ? "fail" : st.finished_at ? "done" : "run"}`}>
            {label(st)}
            {st.started_at && st.finished_at && <span className="t"> · {msLabel(Date.parse(st.finished_at) - Date.parse(st.started_at))}</span>}
          </span>
        </Fragment>
      ))}
    </div>
  );
}

/** Terraform's errors for one resource: the first line, then all of it (R4.3). */
function ResourceErrors({ errors, link, onShowInLog }: { errors: ApplyError[]; link: (e: ApplyError) => string | null; onShowInLog: () => void }) {
  const [more, setMore] = useState(false);
  const first = errors[0];
  const detailLines = (first.detail ?? "").split("\n");
  const hasMore = detailLines.length > 1 || errors.length > 1;
  const shown = more ? errors : [{ ...first, detail: detailLines[0] }];
  const url = link(first);
  const where = first.file ? `${first.file}${first.line ? `:${first.line}` : ""}` : "";
  return (
    <div className="res-error">
      {shown.map((e, i) => (
        <div key={i} className="res-error-item">
          <span className="res-error-title">{e.summary}</span>
          {e.detail && <pre>{e.detail}</pre>}
        </div>
      ))}
      <div className="res-error-foot">
        {where && <span className="mono small muted">{where}</span>}
        {hasMore && (
          <button type="button" className="link" aria-expanded={more} onClick={() => setMore(!more)}>
            {more ? "Show less" : "Show more"}
          </button>
        )}
        <span className="grow" />
        <button type="button" className="small-btn" onClick={onShowInLog}>
          Show in log
        </button>
        {url && (
          <a className="small-btn" href={url} target="_blank" rel="noreferrer">
            <ExternalIcon /> Open {where} in Git
          </a>
        )}
      </div>
    </div>
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

// StateCard shows where each unit keeps its state, as the plan found after
// init (spec 0002 R2.3): Firstlume first, then each other backend with its units.
function StateCard({ run }: { run: Run }) {
  const { link } = useOrg();
  const found = run.unit_backends;
  const groups = new Map<string, string[]>();
  for (const b of found ?? []) groups.set(b.backend, [...(groups.get(b.backend) ?? []), b.unit]);
  const order = [...groups.keys()].sort((a, b) => (a === "firstlume" ? -1 : b === "firstlume" ? 1 : a.localeCompare(b)));
  const single = found?.length === 1 && found[0].unit === "";
  const resources = (
    <Link className="small" to={link(`s/${run.stack_id}/resources`)}>
      See resources
    </Link>
  );
  let empty = "";
  if (!found) {
    const planned = run.status !== "queued" && run.status !== "planning";
    empty = !planned
      ? `Read after Init.${run.config?.run_all ? " With run --all, when the plan finishes." : ""}`
      : run.status === "failed" || run.status === "canceled"
        ? "Not recorded: the plan stopped before reading the backends, or the run is older than this record."
        : "Not recorded: this run is older than the per-unit record. Its Init log still lists the backends.";
  }
  return (
    <section className={found?.some((b) => b.backend === "local") ? "card side-card state-card failed" : "card side-card state-card"} aria-label="State">
      <h2 className="card-label">State</h2>
      {empty && <p className="muted small">{empty}</p>}
      {single && (
        <>
          <div className="state-group-head">
            <BackendBadge backend={found[0].backend} />
            <span className="grow" />
            {found[0].backend === "firstlume" && resources}
          </div>
          <p className="muted small">{backendNote(found[0].backend, true)}</p>
        </>
      )}
      {!single && found && (
        <>
          <p className="muted small">Where each unit keeps its state, read after Terragrunt initialized it.</p>
          {order.map((b) => {
            const units = groups.get(b)!;
            const list = (
              <ul className="state-units">
                {units.map((u) => (
                  <li key={u} className="mono">
                    {u || "(root)"}
                  </li>
                ))}
              </ul>
            );
            return (
              <div key={b} className="state-group">
                <div className="state-group-head">
                  <BackendBadge backend={b} />
                  <strong>
                    {units.length} unit{units.length === 1 ? "" : "s"}
                  </strong>
                  <span className="grow" />
                  {b === "firstlume" && resources}
                </div>
                {b === "firstlume" ? (
                  <details>
                    <summary className="small">Show units</summary>
                    {list}
                  </details>
                ) : (
                  <>
                    {list}
                    <p className={b === "local" ? "danger-text small" : "muted small"}>{backendNote(b, false)}</p>
                  </>
                )}
              </div>
            );
          })}
        </>
      )}
    </section>
  );
}

function BackendBadge({ backend }: { backend: string }) {
  if (backend === "firstlume") return <span className="badge ok">First Lume</span>;
  if (backend === "local") return <span className="badge bad">local</span>;
  return <span className="badge idle">{backend === "http" ? "http · another server" : backend}</span>;
}

function backendNote(backend: string, single: boolean): string {
  switch (backend) {
    case "firstlume":
      return "backend “http”, read after Init.";
    case "local":
      return "On the runner’s disk, which is gone when the run ends. The plan stops here (see the Plan log).";
    case "http":
      return single
        ? "backend “http” with an address that isn’t this First Lume. First Lume doesn’t read this state."
        : "backend “http” on another server. First Lume doesn’t read, lock or version this state.";
  }
  return "First Lume doesn’t read, lock or version this state.";
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
