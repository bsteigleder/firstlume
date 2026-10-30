import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { api, isTerminal, streamLogs, type Run } from "../api";
import { duration, ErrorBanner, PageHeader, PlanCounts, shortID, StatusBadge, timeAgo, useAction, useData } from "../ui";

export function RunPage() {
  const { id = "" } = useParams();
  const run = useData(() => api.getRun(id), [id]);
  const env = useData(async () => (run.data ? api.getEnvironment(run.data.environment_id) : null), [run.data?.environment_id]);
  const action = useAction();
  const status = run.data?.status;

  // Keep the run fresh while it can still change.
  useEffect(() => {
    if (!status || isTerminal(status)) return;
    const t = setInterval(() => void run.reload(), 2000);
    return () => clearInterval(t);
  }, [status, run.reload]);

  const logs = useLogStream(id, run.data, run.reload);

  async function act(fn: (id: string) => Promise<Run>) {
    await action.run(async () => {
      await fn(id);
      await run.reload();
    });
  }

  if (run.error && !run.data) return <ErrorBanner error={run.error} />;
  if (!run.data) return null;
  const r = run.data;

  return (
    <>
      <div className="breadcrumb">
        <Link to="/environments">Environments</Link> /{" "}
        {env.data ? <Link to={`/environments/${env.data.id}`}>{env.data.name}</Link> : "…"} /
      </div>
      <PageHeader
        title={
          <span className="run-title">
            Run <span className="mono">{shortID(r.id)}</span> <StatusBadge status={r.status} />
          </span>
        }
        actions={
          (r.status === "queued" || r.status === "apply_queued") && (
            <button onClick={() => act(api.cancelRun)} disabled={action.busy}>
              Cancel
            </button>
          )
        }
      />
      <ErrorBanner error={action.error} onDismiss={() => action.setError(null)} />

      {r.status === "awaiting_approval" && (
        <div className="approval">
          <div>
            <strong>This plan is waiting for approval.</strong>
            <div className="approval-summary">
              <PlanCounts summary={r.plan_summary} />
              <span className="muted">
                Applying uses exactly this plan, from commit <span className="mono">{r.commit_sha?.slice(0, 7)}</span>.
              </span>
            </div>
          </div>
          <div className="actions">
            <button onClick={() => act(api.discardRun)} disabled={action.busy}>
              Discard
            </button>
            <button className="primary" onClick={() => act(api.approveRun)} disabled={action.busy}>
              Approve &amp; apply
            </button>
          </div>
        </div>
      )}

      {r.status === "drifted" && (
        <div className="notice notice-attention">
          <strong>Drift detected.</strong> The infrastructure no longer matches the code. Nothing was changed: review the plan below, then fix the
          code or run Plan &amp; apply on the environment to reconcile.
        </div>
      )}

      {r.error && (
        <div className="error-banner">
          <span>
            <strong>Run failed:</strong> {r.error}
          </span>
        </div>
      )}

      <dl className="facts">
        <div>
          <dt>Type</dt>
          <dd>{r.type}</dd>
        </div>
        <div>
          <dt>Changes</dt>
          <dd>
            <PlanCounts summary={r.plan_summary} />
          </dd>
        </div>
        <div>
          <dt>Ref</dt>
          <dd className="mono">{r.ref}</dd>
        </div>
        <div>
          <dt>Commit</dt>
          <dd className="mono">{r.commit_sha?.slice(0, 12) ?? "—"}</dd>
        </div>
        <div>
          <dt>Duration</dt>
          <dd>{duration(r.started_at, r.finished_at)}</dd>
        </div>
        <div>
          <dt>Created</dt>
          <dd title={r.created_at}>
            {timeAgo(r.created_at)} by {r.created_by}
          </dd>
        </div>
        {r.approved_by && (
          <div>
            <dt>Approved</dt>
            <dd title={r.approved_at}>
              {timeAgo(r.approved_at)} by {r.approved_by}
            </dd>
          </div>
        )}
      </dl>

      <LogViewer text={logs.text} live={logs.streaming} error={logs.error} waiting={r.status === "queued" || r.status === "apply_queued"} />
    </>
  );
}

/**
 * Streams the run's logs. The server ends the stream when the run finishes or
 * stops for approval; the stream resumes from the last chunk once the run moves on.
 */
function useLogStream(runID: string, run: Run | null, reloadRun: () => Promise<void>) {
  const [text, setText] = useState("");
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);
  const cursor = useRef("");

  useEffect(() => {
    cursor.current = "";
    setText("");
  }, [runID]);

  useEffect(() => {
    const ctrl = new AbortController();
    setStreaming(true);
    streamLogs(
      runID,
      cursor.current,
      {
        onLog: (_phase, data) => setText((t) => t + data),
        onEnd: () => {},
      },
      ctrl.signal,
    )
      .then((c) => {
        cursor.current = c;
        setError(null);
      })
      .catch(async (e) => {
        if (ctrl.signal.aborted) return;
        setError(String(e?.message ?? e));
        await new Promise((r) => setTimeout(r, 3000)); // back off before the effect below retries
      })
      .finally(() => {
        if (ctrl.signal.aborted) return;
        setStreaming(false);
        void reloadRun();
      });
    return () => ctrl.abort();
  }, [runID, generation]);

  const status = run?.status;
  useEffect(() => {
    if (!streaming && status && !isTerminal(status) && status !== "awaiting_approval") {
      setGeneration((g) => g + 1);
    }
  }, [streaming, status]);

  return { text, streaming, error };
}

function LogViewer({ text, live, error, waiting }: { text: string; live: boolean; error: string | null; waiting: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const lines = useMemo(() => text.replace(/\n$/, "").split("\n"), [text]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && follow) el.scrollTop = el.scrollHeight;
  }, [lines, follow]);

  function onScroll() {
    const el = ref.current;
    if (el) setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
  }

  return (
    <section className="logs">
      <div className="logs-header">
        <h2>Logs</h2>
        <span className="muted">{live ? (waiting ? "Waiting for an agent…" : "Streaming") : ""}</span>
        {live && !waiting && <span className="pulse" aria-hidden />}
        {error && <span className="form-error">{error}</span>}
      </div>
      <div className="log-view" ref={ref} onScroll={onScroll}>
        {text === "" ? (
          <div className="log-line muted">{waiting ? "Queued. The logs appear when an agent picks up the run." : "No output yet."}</div>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={`log-line${l.startsWith("==>") ? " log-step" : ""}${/^(Error|│ Error)/.test(l) ? " log-error" : ""}`}>
              {l || " "}
            </div>
          ))
        )}
      </div>
    </section>
  );
}
