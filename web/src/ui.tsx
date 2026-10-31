import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { Drift, Level, PlanSummary, RunStatus } from "./api";
import { CloseIcon } from "./icons";

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
  queued: "idle",
  planning: "run",
  awaiting_approval: "warn",
  apply_queued: "idle",
  applying: "run",
  planned: "ok",
  no_changes: "ok",
  drifted: "warn",
  applied: "ok",
  failed: "bad",
  discarded: "idle",
  canceled: "idle",
};

export function statusLabel(s: RunStatus) {
  return statusLabels[s];
}

export function StatusBadge({ status, title }: { status: RunStatus; title?: string }) {
  const tone = statusTone[status];
  return (
    <span className={`badge ${tone}`} title={title}>
      {tone === "run" && <span className="dot pulse" aria-hidden />}
      {statusLabels[status]}
    </span>
  );
}

/** A stack's drift state, from its latest finished drift check. */
export function DriftBadge({ drift }: { drift?: Drift }) {
  if (!drift) return <span className="muted">Not checked</span>;
  const [tone, label] = drift.status === "drifted" ? ["warn", "Drifted"] : drift.status === "error" ? ["bad", "Check failed"] : ["ok", "In sync"];
  return <span className={`badge ${tone}`}>{label}</span>;
}

export function PlanCounts({ summary }: { summary?: PlanSummary }) {
  if (!summary) return <span className="muted">—</span>;
  return (
    <span className="plan-counts mono">
      <span className="add">+{summary.add}</span>
      <span className="chg">~{summary.change}</span>
      {summary.replace > 0 && <span className="rep">±{summary.replace}</span>}
      <span className="del">−{summary.destroy}</span>
    </span>
  );
}

export const driftIntervals: { minutes: number; label: string }[] = [
  { minutes: 0, label: "Off" },
  { minutes: 60, label: "Every hour" },
  { minutes: 360, label: "Every 6 hours" },
  { minutes: 720, label: "Every 12 hours" },
  { minutes: 1440, label: "Every day" },
  { minutes: 10080, label: "Every week" },
];

export function driftLabel(minutes: number) {
  return driftIntervals.find((d) => d.minutes === minutes)?.label ?? `Every ${minutes} min`;
}

export function timeAgo(iso?: string): string {
  if (!iso) return "—";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return new Date(iso).toLocaleDateString();
}

export function duration(from?: string, to?: string): string {
  if (!from) return "—";
  const ms = (to ? new Date(to).getTime() : Date.now()) - new Date(from).getTime();
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`;
}

export function shortID(id: string) {
  return id.slice(0, 8);
}

/** Shortens a repository URL for display: host/owner/repo. */
export function repoLabel(repo: string) {
  return repo.replace(/^https?:\/\//, "").replace(/^ssh:\/\/(git@)?/, "").replace(/^git@([^:]+):/, "$1/").replace(/\.git$/, "");
}

/** The repository's web address, when it can be derived (https or scp-like ssh URLs). */
export function repoWebURL(repo: string): string | null {
  let m = repo.match(/^(https?):\/\/(.+?)(\.git)?\/?$/);
  if (m) return `${m[1]}://${m[2]}`;
  m = repo.match(/^(?:ssh:\/\/)?git@([^:/]+)[:/](.+?)(\.git)?$/);
  if (m) return `https://${m[1]}/${m[2]}`;
  return null;
}

export function commitURL(repo: string, sha?: string): string | null {
  const web = repoWebURL(repo);
  return web && sha ? `${web}/commit/${sha}` : null;
}

/** Link to a file at a commit: GitHub and GitLab use blob paths, Gitea src/commit. */
export function fileURL(repo: string, sha: string | undefined, path: string): string | null {
  const web = repoWebURL(repo);
  if (!web || !sha) return null;
  if (web.includes("github.com/")) return `${web}/blob/${sha}/${path}`;
  if (web.includes("gitlab")) return `${web}/-/blob/${sha}/${path}`;
  return `${web}/src/commit/${sha}/${path}`;
}

const typeNames: Record<string, string> = {
  aws_vpc: "VPC",
  aws_subnet: "Subnet",
  aws_security_group: "Security group",
  aws_security_group_rule: "Security group rule",
  aws_db_instance: "Database (RDS instance)",
  aws_rds_cluster: "Database cluster (RDS)",
  aws_instance: "EC2 instance",
  aws_s3_bucket: "S3 bucket",
  aws_iam_role: "IAM role",
  aws_iam_policy: "IAM policy",
  aws_route_table: "Route table",
  aws_route_table_association: "Route table association",
  aws_lb: "Load balancer",
  aws_eks_cluster: "EKS cluster",
  google_compute_network: "VPC network",
  google_compute_subnetwork: "Subnet",
  google_sql_database_instance: "Database (Cloud SQL)",
  google_storage_bucket: "Storage bucket",
  azurerm_virtual_network: "Virtual network",
  azurerm_subnet: "Subnet",
  kubernetes_namespace: "Kubernetes namespace",
  kubernetes_manifest: "Kubernetes manifest",
  kubernetes_secret: "Kubernetes secret",
  kubernetes_deployment: "Kubernetes deployment",
  helm_release: "Helm release",
  lxd_instance: "LXD instance",
  null_resource: "Null resource",
  terraform_data: "Terraform data",
};

/** A readable name for a resource type ("aws_db_instance" → "Database (RDS instance)"). */
export function humanType(type: string): string {
  if (typeNames[type]) return typeNames[type];
  const rest = type.includes("_") ? type.slice(type.indexOf("_") + 1) : type;
  const words = rest.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function levelName(l: Level): string {
  if (l.kind === "default") return "default";
  return l.name || l.kind;
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

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function Initials({ name, size = 24 }: { name?: string; size?: number }) {
  const n = (name || "?").trim();
  const parts = n.split(/[\s._-]+/).filter(Boolean);
  const text = ((parts[0]?.[0] ?? "?") + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase();
  let h = 0;
  for (const c of n) h = (h * 31 + c.charCodeAt(0)) % 360;
  return (
    <span className="avatar" style={{ width: size, height: size, fontSize: size * 0.42, background: `hsl(${h} 32% 42%)` }} aria-hidden>
      {text}
    </span>
  );
}

/** A centered dialog over a dimmed page. Escape and the backdrop close it. */
export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <section role="dialog" aria-modal="true" aria-label={title} className={`modal${wide ? " wide" : ""}`}>
        <header className="modal-header">
          <h2>{title}</h2>
          <button className="icon-btn" aria-label="Close" onClick={onClose}>
            <CloseIcon />
          </button>
        </header>
        {children}
      </section>
    </div>
  );
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

/** Suggests an organization address from its name. */
export function slugify(name: string): string {
  return name
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
}
