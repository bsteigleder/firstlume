import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { api, setToken, type ActiveRun, type Folder, type OrgAPI, type Organization, type StackSummary, type Tree } from "./api";
import { ChevronDownIcon, ChevronRightIcon, CloseIcon, FolderIcon, FolderPlusIcon, GearIcon, PlusIcon, SearchIcon } from "./icons";
import { DeleteFolderDialog, FolderNameDialog, NewStackDialog } from "./pages/dialogs";
import { ErrorBanner, Initials, StatusBadge, timeAgo, useData } from "./ui";
import { ThemeSwitch } from "./theme";

export interface Crumb {
  label: string;
  to?: string;
}

type Dialog =
  | { kind: "new-folder"; parentID: string }
  | { kind: "rename-folder"; folder: Folder }
  | { kind: "delete-folder"; folder: Folder }
  | { kind: "new-stack"; folderID: string };

interface OrgContextValue {
  org: Organization;
  api: OrgAPI;
  tree: Tree | null;
  reloadTree: () => Promise<void>;
  active: ActiveRun[];
  reloadActive: () => Promise<void>;
  /** Folders from the top level down to (and including) the given one. */
  folderChain: (folderID?: string) => Folder[];
  folderLabel: (folderID?: string) => string;
  open: (d: Dialog) => void;
  setCrumbs: (c: Crumb[]) => void;
  link: (path?: string) => string;
}

const OrgContext = createContext<OrgContextValue | null>(null);

export function useOrg(): OrgContextValue {
  const v = useContext(OrgContext);
  if (!v) throw new Error("useOrg outside an organization");
  return v;
}

/** Sets the header breadcrumb for the current page. */
export function useCrumbs(crumbs: Crumb[]) {
  const { setCrumbs } = useOrg();
  const key = JSON.stringify(crumbs);
  useEffect(() => {
    setCrumbs(crumbs);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, setCrumbs]);
}

const lastOrgKey = "firstlume.org";

export function rememberOrg(slug: string) {
  try {
    localStorage.setItem(lastOrgKey, slug);
  } catch {
    // only a convenience
  }
}

export function lastOrg(): string | null {
  try {
    return localStorage.getItem(lastOrgKey);
  } catch {
    return null;
  }
}

export function OrgShell() {
  const { org: slug = "" } = useParams();
  const orgAPI = useMemo(() => api.org(slug), [slug]);
  const org = useData(() => orgAPI.get(), [orgAPI]);
  const tree = useData(() => orgAPI.tree(), [orgAPI], { poll: true, pollMs: 6000 });
  const active = useData(() => orgAPI.activeRuns(), [orgAPI], { poll: true, pollMs: 4000 });
  const [crumbs, setCrumbs] = useState<Crumb[]>([]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [drawer, setDrawer] = useState(false);

  useEffect(() => rememberOrg(slug), [slug]);

  const folderChain = useCallback(
    (folderID?: string) => {
      if (!folderID || !tree.data) return [];
      const f = tree.data.folders.find((x) => x.id === folderID);
      if (!f) return [];
      return f.path.map((id) => tree.data!.folders.find((x) => x.id === id)).filter((x): x is Folder => !!x);
    },
    [tree.data],
  );

  if (org.error && !org.data) {
    return (
      <div className="page-center">
        <ErrorBanner error={org.error} />
        <Link to="/">Back to your organizations</Link>
      </div>
    );
  }
  if (!org.data) return null;

  const value: OrgContextValue = {
    org: org.data,
    api: orgAPI,
    tree: tree.data,
    reloadTree: tree.reload,
    active: active.data ?? [],
    reloadActive: active.reload,
    folderChain,
    folderLabel: (id) => folderChain(id).map((f) => f.name).join(" / ") || org.data!.name,
    open: setDialog,
    setCrumbs,
    link: (path = "") => `/o/${slug}${path ? `/${path}` : ""}`,
  };

  return (
    <OrgContext.Provider value={value}>
      <div className="shell">
        <Sidebar />
        <div className="main-col">
          <Header crumbs={crumbs} onOpenRuns={() => setDrawer(true)} />
          <main className="content">
            <Outlet />
          </main>
        </div>
      </div>
      {drawer && <ActiveRunsDrawer onClose={() => setDrawer(false)} />}
      {dialog?.kind === "new-folder" && <FolderNameDialog parentID={dialog.parentID} onClose={() => setDialog(null)} />}
      {dialog?.kind === "rename-folder" && <FolderNameDialog folder={dialog.folder} onClose={() => setDialog(null)} />}
      {dialog?.kind === "delete-folder" && <DeleteFolderDialog folder={dialog.folder} onClose={() => setDialog(null)} />}
      {dialog?.kind === "new-stack" && <NewStackDialog folderID={dialog.folderID} onClose={() => setDialog(null)} />}
    </OrgContext.Provider>
  );
}

function Header({ crumbs, onOpenRuns }: { crumbs: Crumb[]; onOpenRuns: () => void }) {
  const { active } = useOrg();
  const running = active.filter((r) => r.status !== "awaiting_approval").length;
  const waiting = active.length - running;
  return (
    <header className="topbar">
      <nav aria-label="Breadcrumb" className="crumbs">
        {crumbs.map((c, i) => (
          <span key={i} className="crumb">
            {i > 0 && <span className="sep">/</span>}
            {c.to && i < crumbs.length - 1 ? <Link to={c.to}>{c.label}</Link> : <span className="current">{c.label}</span>}
          </span>
        ))}
      </nav>
      <button className="runs-pill" onClick={onOpenRuns} aria-label={`Active runs: ${running} running, ${waiting} need approval`}>
        <span className={`badge ${running ? "run" : "idle"}`}>
          {running > 0 && <span className="dot pulse" aria-hidden />}
          {running} running
        </span>
        <span className={`badge ${waiting ? "warn" : "idle"}`}>{waiting} need approval</span>
      </button>
      <span className="who" title="Signed in with the admin token">
        <Initials name="admin" size={28} />
      </span>
    </header>
  );
}

function Sidebar() {
  const { org, tree, link, open } = useOrg();
  const [query, setQuery] = useState("");
  const [switcher, setSwitcher] = useState(false);
  const location = useLocation();

  return (
    <aside className="sidebar">
      <div className="org-switch">
        <button className="org-button" aria-expanded={switcher} onClick={() => setSwitcher((s) => !s)}>
          <span className="org-mark">{org.name.charAt(0).toUpperCase()}</span>
          <span className="org-text">
            <strong>{org.name}</strong>
            <span>Organization</span>
          </span>
          <ChevronDownIcon />
        </button>
        {switcher && <OrgSwitcher onClose={() => setSwitcher(false)} />}
      </div>

      <label className="search">
        <SearchIcon />
        <input type="search" placeholder="Find a stack or folder" value={query} onChange={(e) => setQuery(e.target.value)} />
      </label>

      <nav aria-label="Folders and stacks" className="tree">
        <div className="tree-root">
          <Link to={link()} className="tree-root-name">
            {org.name}
          </Link>
          <span className="row-actions always">
            <button className="icon-btn tip" data-tip={`New stack at the top level of ${org.name}`} aria-label="New stack at the top level"
              onClick={() => open({ kind: "new-stack", folderID: "" })}>
              <PlusIcon />
            </button>
            <button className="icon-btn tip" data-tip={`New folder at the top level of ${org.name}`} aria-label="New folder at the top level"
              onClick={() => open({ kind: "new-folder", parentID: "" })}>
              <FolderPlusIcon />
            </button>
            <Link className="icon-btn tip" data-tip="Settings" aria-label="Organization settings" to={link("settings")}>
              <GearIcon />
            </Link>
          </span>
        </div>
        {tree ? <TreeLevel parentID="" depth={0} query={query.trim().toLowerCase()} path={location.pathname} /> : null}
        {tree && tree.folders.length === 0 && tree.stacks.length === 0 && (
          <p className="muted small tree-empty">Nothing here yet. Use + to add a stack or a folder.</p>
        )}
      </nav>

      <div className="sidebar-footer">
        <NavLink to={link("settings")} className="footer-link">
          Agents and settings
        </NavLink>
        <ThemeSwitch />
        <button
          className="link"
          onClick={() => {
            setToken(null);
            window.location.assign("/");
          }}
        >
          Sign out
        </button>
      </div>
    </aside>
  );
}

function matches(tree: Tree, folder: Folder, q: string): boolean {
  if (folder.name.toLowerCase().includes(q)) return true;
  if (tree.stacks.some((s) => s.folder_id === folder.id && s.name.toLowerCase().includes(q))) return true;
  return tree.folders.some((f) => f.parent_id === folder.id && matches(tree, f, q));
}

function stackDot(s: StackSummary): string {
  const st = s.last_run?.status;
  if (st === "failed") return "var(--danger)";
  if (st === "awaiting_approval" || s.drift?.status === "drifted") return "var(--attention)";
  if (st === "planning" || st === "applying" || st === "queued" || st === "apply_queued") return "var(--running)";
  if (st) return "var(--success)";
  return "var(--border-strong)";
}

function TreeLevel({ parentID, depth, query, path }: { parentID: string; depth: number; query: string; path: string }) {
  const { tree, link, open, folderChain } = useOrg();
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  if (!tree) return null;

  const folders = tree.folders.filter((f) => (f.parent_id ?? "") === parentID && (!query || matches(tree, f, query)));
  const stacks = tree.stacks.filter((s) => (s.folder_id ?? "") === parentID && (!query || s.name.toLowerCase().includes(query)));
  const pad = { paddingLeft: 6 + depth * 16 };

  return (
    <>
      {folders.map((f) => {
        const selected = path === link(`f/${f.id}`) || path === link(`f/${f.id}/settings`);
        const onPath = path.includes(f.id) || tree.stacks.some((s) => s.folder_id && path.includes(s.id) && folderChain(s.folder_id).some((x) => x.id === f.id));
        const isOpen = query ? true : (expanded[f.id] ?? (onPath || depth === 0));
        return (
          <div key={f.id}>
            <div className={`tree-row${selected ? " selected" : ""}`} style={pad}>
              <button className="chev" aria-label={isOpen ? `Collapse ${f.name}` : `Expand ${f.name}`} aria-expanded={isOpen}
                onClick={() => setExpanded((e) => ({ ...e, [f.id]: !isOpen }))}>
                {isOpen ? <ChevronDownIcon size={12} /> : <ChevronRightIcon />}
              </button>
              <Link to={link(`f/${f.id}`)} className="tree-link">
                <FolderIcon />
                <span className="ellipsis">{f.name}</span>
              </Link>
              <span className="row-actions">
                <button className="icon-btn tip" data-tip={`New stack in ${f.name}`} aria-label={`New stack in ${f.name}`}
                  onClick={() => open({ kind: "new-stack", folderID: f.id })}>
                  <PlusIcon />
                </button>
                {f.path.length < 10 && (
                  <button className="icon-btn tip" data-tip={`New folder inside ${f.name}`} aria-label={`New folder in ${f.name}`}
                    onClick={() => open({ kind: "new-folder", parentID: f.id })}>
                    <FolderPlusIcon />
                  </button>
                )}
                <Link className="icon-btn tip" data-tip="Settings" aria-label={`Settings of ${f.name}`} to={link(`f/${f.id}/settings`)}>
                  <GearIcon />
                </Link>
              </span>
            </div>
            {isOpen && <TreeLevel parentID={f.id} depth={depth + 1} query={query} path={path} />}
          </div>
        );
      })}
      {stacks.map((s) => {
        const selected = path.startsWith(link(`s/${s.id}`));
        return (
          <div key={s.id} className={`tree-row${selected ? " selected" : ""}`} style={pad}>
            <span className="chev" aria-hidden />
            <Link to={link(`s/${s.id}`)} className="tree-link">
              <span className="stack-dot" style={{ background: stackDot(s) }} aria-hidden />
              <span className="ellipsis">{s.name}</span>
            </Link>
          </div>
        );
      })}
    </>
  );
}

function OrgSwitcher({ onClose }: { onClose: () => void }) {
  const { org } = useOrg();
  const orgs = useData(api.listOrgs, []);
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  const list = (orgs.data ?? []).filter((o) => !q || `${o.name} ${o.slug}`.toLowerCase().includes(q.toLowerCase()));
  return (
    <div className="menu org-menu" role="menu">
      <label className="search small-search">
        <SearchIcon size={13} />
        <input type="search" placeholder="Find organization" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
      </label>
      {list.map((o) => (
        <button key={o.id} role="menuitem" className={`menu-item${o.id === org.id ? " current" : ""}`}
          onClick={() => {
            onClose();
            navigate(`/o/${o.slug}`);
          }}>
          <span className="org-mark small">{o.name.charAt(0).toUpperCase()}</span>
          <span className="org-text">
            <strong>{o.name}</strong>
            <span className="mono">{o.slug}</span>
          </span>
          {o.id === org.id && <span aria-hidden>✓</span>}
        </button>
      ))}
      <div className="menu-sep" />
      <button role="menuitem" className="menu-item" onClick={() => navigate("/new-org")}>
        <PlusIcon /> Create organization
      </button>
    </div>
  );
}

function ActiveRunsDrawer({ onClose }: { onClose: () => void }) {
  const { active, link } = useOrg();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  const groups: { title: string; runs: ActiveRun[] }[] = [
    { title: "Needs approval", runs: active.filter((r) => r.status === "awaiting_approval") },
    { title: "Running", runs: active.filter((r) => r.status === "planning" || r.status === "applying") },
    { title: "Queued", runs: active.filter((r) => r.status === "queued" || r.status === "apply_queued") },
  ].filter((g) => g.runs.length > 0);

  return (
    <div className="drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="drawer" aria-label="Active runs">
        <header className="drawer-header">
          <h2>
            Active runs <span className="muted">· {active.length}</span>
          </h2>
          <button className="icon-btn" aria-label="Close" onClick={onClose}>
            <CloseIcon />
          </button>
        </header>
        <div className="drawer-body">
          {groups.length === 0 && <p className="empty-note">Nothing running right now.</p>}
          {groups.map((g) => (
            <section key={g.title}>
              <h3 className="group-title">{g.title}</h3>
              {g.runs.map((r) => (
                <Link key={r.id} to={link(`r/${r.id}`)} onClick={onClose} className={`queue-row${r.status === "awaiting_approval" ? " attention" : ""}`}>
                  <Initials name={r.commit_author_name || r.created_by} size={30} />
                  <span className="queue-main">
                    <span className="queue-title">
                      <strong>{r.stack_name}</strong>
                      <span className="muted small">{r.folder_path.length ? r.folder_path.join(" / ") : "top level"}</span>
                    </span>
                    <span className="ellipsis">{r.commit_message || `${r.type} on ${r.ref}`}</span>
                    <span className="muted small">
                      {r.type} · {r.commit_author_name ? `${r.commit_author_name} · ` : ""}started by {r.created_by}
                    </span>
                  </span>
                  <span className="queue-side">
                    <StatusBadge status={r.status} />
                    <span className="muted small">{timeAgo(r.created_at)}</span>
                  </span>
                </Link>
              ))}
            </section>
          ))}
        </div>
      </aside>
    </div>
  );
}

export function PageTitle({ title, sub, actions }: { title: ReactNode; sub?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-title">
      <div className="page-title-text">
        <h1>{title}</h1>
        {sub && <div className="sub">{sub}</div>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}
