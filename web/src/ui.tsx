import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { Drift, PlanSummary, RunStatus } from "./api";

const statusLabels: Record<RunStatus, string> = {
  queued: "Queued",
  planning: "Planning",
  awaiting_approval: "Awaiting approval",
  apply_queued: "Apply queued",
  applying: "Applying",
  planned: "Planned",
  no_changes: "No changes",
  drifted: "Drift detected",
  applied: "Applied",
  failed: "Failed",
  discarded: "Discarded",
  canceled: "Canceled",
};

const statusTone: Record<RunStatus, string> = {
  queued: "neutral",
  planning: "running",
  awaiting_approval: "attention",
  apply_queued: "neutral",
  applying: "running",
  planned: "success",
  no_changes: "success",
  drifted: "attention",
  applied: "success",
  failed: "danger",
  discarded: "neutral",
  canceled: "neutral",
};

export function StatusBadge({ status }: { status: RunStatus }) {
  const tone = statusTone[status];
  return (
    <span className={`badge badge-${tone}`}>
      {tone === "running" && <span className="pulse" aria-hidden />}
      {statusLabels[status]}
    </span>
  );
}

/** The environment's drift state, from its latest finished drift check. */
export function DriftBadge({ drift, intervalMinutes }: { drift?: Drift; intervalMinutes: number }) {
  if (!drift) return <span className="muted">{intervalMinutes ? "Not checked yet" : "Off"}</span>;
  const [tone, label] =
    drift.status === "drifted" ? ["attention", "Drifted"] : drift.status === "error" ? ["danger", "Check failed"] : ["success", "In sync"];
  return <span className={`badge badge-${tone}`}>{label}</span>;
}

export const driftIntervals: { minutes: number; label: string }[] = [
  { minutes: 0, label: "Off" },
  { minutes: 60, label: "Every hour" },
  { minutes: 360, label: "Every 6 hours" },
  { minutes: 720, label: "Every 12 hours" },
  { minutes: 1440, label: "Every day" },
  { minutes: 10080, label: "Every week" },
];

export function PlanCounts({ summary }: { summary?: PlanSummary }) {
  if (!summary) return <span className="muted">—</span>;
  return (
    <span className="plan-counts">
      <span className="add">+{summary.add}</span>
      <span className="change">~{summary.change}</span>
      <span className="destroy">−{summary.destroy}</span>
    </span>
  );
}

export function timeAgo(iso?: string): string {
  if (!iso) return "—";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return new Date(iso).toLocaleDateString();
}

export function duration(from?: string, to?: string): string {
  if (!from) return "—";
  const ms = (to ? new Date(to).getTime() : Date.now()) - new Date(from).getTime();
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

/** Shortens a repository URL for display: github.com/acme/infra rather than https://github.com/acme/infra.git. */
export function repoLabel(repo: string) {
  return repo.replace(/^https:\/\//, "").replace(/\.git$/, "");
}

export function shortID(id: string) {
  return id.slice(0, 8);
}

export function ErrorBanner({ error, onDismiss }: { error: string | null; onDismiss?: () => void }) {
  if (!error) return null;
  return (
    <div className="error-banner" role="alert">
      <span>{error}</span>
      {onDismiss && (
        <button className="link" onClick={onDismiss}>
          Dismiss
        </button>
      )}
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div>
        <h1>{title}</h1>
        {subtitle && <div className="subtitle">{subtitle}</div>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </header>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

/** Loads data with `load`, reloading every `pollMs` while `poll` is true. */
export function useData<T>(load: () => Promise<T>, deps: unknown[], opts: { pollMs?: number; poll?: boolean } = {}) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const loadRef = useRef(load);
  loadRef.current = load;

  const reload = useCallback(async () => {
    try {
      setData(await loadRef.current());
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, deps);

  useEffect(() => {
    setData(null);
    void reload();
  }, [reload]);

  const { pollMs = 3000, poll = false } = opts;
  useEffect(() => {
    if (!poll) return;
    const t = setInterval(() => void reload(), pollMs);
    return () => clearInterval(t);
  }, [poll, pollMs, reload]);

  return { data, error, reload, setError };
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** Runs an action, tracking whether it is in flight and any error it raised. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      return true;
    } catch (e) {
      setError(errorMessage(e));
      return false;
    } finally {
      setBusy(false);
    }
  }, []);
  return { busy, error, setError, run };
}
