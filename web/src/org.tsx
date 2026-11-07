import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { api, signOut, type ActiveRun, type Folder, type Me, type OrgAPI, type Organization, type Stack, type StackSummary, type Tree, type User } from "./api";
import { ChevronDownIcon, ChevronRightIcon, CloseIcon, FolderIcon, FolderPlusIcon, GearIcon, Logo, PlusIcon, SearchIcon, TrashIcon } from "./icons";
import { DeleteFolderDialog, FolderNameDialog, NewStackDialog } from "./pages/dialogs";
import { MoveStackDialog, StackGrip, useStackDrop } from "./pages/MoveStack";
import { errorMessage, ErrorBanner, Initials, runOriginText, StatusBadge, timeAgo, useData } from "./ui";
import { useSession } from "./session";
import { ThemeSwitch } from "./theme";

export interface Crumb {
  label: string;
  to?: string;
}

type Dialog =
  | { kind: "new-folder"; parentID: string }
  | { kind: "rename-folder"; folder: Folder }
  | { kind: "delete-folder"; folder: Folder }
  | { kind: "new-stack"; folderID: string }
  | { kind: "move-stack"; stack: Stack; folderID: string };

interface Toast {
  text: ReactNode;
  undo?: () => Promise<void>;
}

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
  /** The stack being dragged by its grip, if any. */
  drag: Stack | null;
  startDrag: (s: Stack) => void;
  endDrag: () => void;
  /** Shows a short message at the bottom, with Undo when undo is given. */
  notify: (text: ReactNode, undo?: () => Promise<void>) => void;
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

/** What sits directly in a folder. Only an empty one (no folders, no stacks) can be deleted. */
export function folderContents(tree: Tree | null, folderID: string) {
  const folders = tree?.folders.filter((f) => f.parent_id === folderID).length ?? 0;
  const stacks = tree?.stacks.filter((s) => s.folder_id === folderID).length ?? 0;
  const parts = [
    folders > 0 && `${folders} folder${folders === 1 ? "" : "s"}`,
    stacks > 0 && `${stacks} stack${stacks === 1 ? "" : "s"}`,
  ].filter(Boolean);
  return { folders, stacks, empty: folders === 0 && stacks === 0, summary: parts.join(" and ") };
}

export function lastOrg(): string | null {
  try {
    return localStorage.getItem(lastOrgKey);
  } catch {
    return null;
  }
}

/** What the API answers for an organization the caller can't see. */
const orgNotFound = "organization not found";

function OrgNotFound() {
  const { me } = useSession();
  return (
    <div className="page-center">
      <h1>Organization not found</h1>
      <p className="muted">
        It doesn’t exist, or you haven’t been invited to it.
        {me.user && (
          <>
            {" "}
            If you need access, ask one of its administrators to invite <span className="mono">{me.user.login}</span>.
          </>
        )}
      </p>
      <Link to="/">Back to your organizations</Link>
    </div>
  );
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
  const [drag, setDrag] = useState<Stack | null>(null);
  const [toast, setToast] = useState<Toast | null>(null);
  const closeToast = useCallback(() => setToast(null), []);

  const navigate = useNavigate();
  const location = useLocation();
  useEffect(() => rememberOrg(slug), [slug]);
  // A former address still resolves: move to the current one.
  const current = org.data?.slug;
  useEffect(() => {
    if (current && current !== slug) {
      navigate(location.pathname.replace(`/o/${slug}`, `/o/${current}`) + location.search + location.hash, { replace: true });
    }
  }, [current, slug, navigate, location]);

  const folderChain = useCallback(
    (folderID?: string) => {
      if (!folderID || !tree.data) return [];
      const f = tree.data.folders.find((x) => x.id === folderID);
      if (!f) return [];
      return f.path.map((id) => tree.data!.folders.find((x) => x.id === id)).filter((x): x is Folder => !!x);
    },
    [tree.data],
  );

  // The same answer for an address that doesn't exist, one they were never
  // invited to, and one they were just removed from (R7.16).
  if (org.error === orgNotFound || tree.error === orgNotFound) return <OrgNotFound />;
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
    drag,
    startDrag: setDrag,
    endDrag: () => setDrag(null),
    notify: (text, undo) => setToast({ text, undo }),
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
      {dialog?.kind === "move-stack" && <MoveStackDialog stack={dialog.stack} folderID={dialog.folderID} onClose={() => setDialog(null)} />}
      {toast && <ToastBar toast={toast} onDone={closeToast} onReplace={setToast} />}
    </OrgContext.Provider>
  );
}

/** A message at the bottom for 8 seconds; Undo runs the toast's undo once. */
function ToastBar({ toast, onDone, onReplace }: { toast: Toast; onDone: () => void; onReplace: (t: Toast) => void }) {
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const t = window.setTimeout(onDone, 8000);
    return () => window.clearTimeout(t);
  }, [toast, onDone]);
  const undo = toast.undo;
  return (
    <div role="status" className="toast">
      <span className="grow">{toast.text}</span>
      {undo && (
        <button
          type="button"
          className="toast-undo"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await undo();
              onDone();
            } catch (e) {
              onReplace({ text: `Undo failed: ${errorMessage(e)}` });
            } finally {
              setBusy(false);
            }
          }}
        >
          Undo
        </button>
      )}
    </div>
  );
}

function Header({ crumbs, onOpenRuns }: { crumbs: Crumb[]; onOpenRuns: () => void }) {
  const { active } = useOrg();
  const waiting = active.filter((r) => r.status === "awaiting_approval").length;
  const failed = active.filter((r) => r.status === "failed").length;
  const running = active.length - waiting - failed;
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
      <button className="runs-pill" onClick={onOpenRuns} aria-label={`Active runs: ${running} running, ${waiting} need approval${failed ? `, ${failed} failed in the last hour` : ""}`}>
        <span className={`badge ${running ? "run" : "idle"}`}>
          {running > 0 && <span className="dot pulse" aria-hidden />}
          {running} running
        </span>
        <span className={`badge ${waiting ? "warn" : "idle"}`}>{waiting} need approval</span>
        {failed > 0 && <span className="badge bad">{failed} failed</span>}
      </button>
      <AccountMenu />
    </header>
  );
}

/** Ends the session, then shows the login screen ("signed out" only for a user). */
export async function signOutAndLeave(me: Me) {
  await signOut();
  window.location.assign(me.kind === "user" ? "/?signed_out=1" : "/");
}

export function Avatar({ user, size }: { user?: User; size: number }) {
  const [broken, setBroken] = useState(false);
  if (user?.avatar_url && !broken) {
    return <img className="avatar" src={user.avatar_url} alt="" width={size} height={size} onError={() => setBroken(true)} />;
  }
  return <Initials name={user ? user.name || user.login : "admin"} size={size} />;
}

/** Who is signed in, their Gitea groups, and Sign out. */
function AccountMenu() {
  const { me } = useSession();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);

  const user = me.user;
  return (
    <div className="account" ref={ref}>
      <button className="account-btn" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Avatar user={user} size={28} />
        <span className="account-login">{user ? user.login : "admin token"}</span>
        <ChevronDownIcon />
      </button>
      {open && (
        <div role="menu" aria-label="Account" className="menu account-menu">
          <div className="account-head">
            <Avatar user={user} size={40} />
            <span className="account-id">
              <strong>{user ? user.name || user.login : "Admin token"}</strong>
              <span className="mono muted small">{user ? `${user.login} · signed in with Gitea` : "for automation and emergencies"}</span>
              {user && me.installation_admin && <span className="badge warn fit">Installation admin</span>}
            </span>
          </div>
          {user && user.groups.length > 0 && (
            <div className="account-groups">
              <span className="small">Gitea groups</span>
              <span className="chips">
                {user.groups.map((g) => (
                  <span key={g} className="chip mono">
                    {g}
                  </span>
                ))}
              </span>
            </div>
          )}
          <div className="menu-sep" />
          <button role="menuitem" className="menu-item" onClick={() => void signOutAndLeave(me)}>
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}

function Sidebar() {
  const { org, tree, link, open } = useOrg();
  const [query, setQuery] = useState("");
  const [switcher, setSwitcher] = useState(false);
  const location = useLocation();

  return (
    <aside className="sidebar">
      <div className="sidebar-brand">
        <Logo size={24} />
        <strong>First Lume</strong>
      </div>
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
        <TreeRoot>
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
        </TreeRoot>
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
        <SidebarSignOut />
      </div>
    </aside>
  );
}

/** The organization's row, which takes stacks dropped to the top level. */
function TreeRoot({ children }: { children: ReactNode }) {
  const drop = useStackDrop("");
  return (
    <div className={`tree-root ${drop.state}`} {...drop.handlers}>
      {children}
      {drop.state === "drop-ok" && <span className="drop-hint">Move to the top level</span>}
    </div>
  );
}

function SidebarSignOut() {
  const { me } = useSession();
  return (
    <button className="link" onClick={() => void signOutAndLeave(me)}>
      Sign out
    </button>
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
  const { tree, link, folderChain, drag } = useOrg();
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  if (!tree) return null;

  const folders = tree.folders.filter((f) => (f.parent_id ?? "") === parentID && (!query || matches(tree, f, query)));
  const stacks = tree.stacks.filter((s) => (s.folder_id ?? "") === parentID && (!query || s.name.toLowerCase().includes(query)));
  const pad = { paddingLeft: 6 + depth * 16 };

  return (
    <>
      {folders.map((f) => {
        const onPath = path.includes(f.id) || tree.stacks.some((s) => s.folder_id && path.includes(s.id) && folderChain(s.folder_id).some((x) => x.id === f.id));
        const isOpen = query ? true : (expanded[f.id] ?? (onPath || depth === 0));
        return (
          <FolderRow key={f.id} folder={f} depth={depth} query={query} path={path} isOpen={isOpen}
            setOpen={(o) => setExpanded((e) => ({ ...e, [f.id]: o }))} />
        );
      })}
      {stacks.map((s) => {
        const selected = path.startsWith(link(`s/${s.id}`));
        return (
          <div key={s.id} className={`tree-row${selected ? " selected" : ""}${drag?.id === s.id ? " dragging" : ""}`} style={pad}>
            <StackGrip stack={s} rowSelector=".tree-row" />
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

/** A folder in the tree: its row, a drop target for stacks, and its contents when open. */
function FolderRow({ folder: f, depth, query, path, isOpen, setOpen }: {
  folder: Folder; depth: number; query: string; path: string; isOpen: boolean; setOpen: (open: boolean) => void;
}) {
  const { tree, link, open, drag } = useOrg();
  const drop = useStackDrop(f.id, () => setOpen(true));
  const selected = path === link(`f/${f.id}`) || path === link(`f/${f.id}/settings`);
  return (
    <div>
      <div className={`tree-row${selected ? " selected" : ""} ${drop.state}`} style={{ paddingLeft: 6 + depth * 16 }} {...drop.handlers}>
        <button className="chev" aria-label={isOpen ? `Collapse ${f.name}` : `Expand ${f.name}`} aria-expanded={isOpen}
          onClick={() => setOpen(!isOpen)}>
          {isOpen ? <ChevronDownIcon size={12} /> : <ChevronRightIcon />}
        </button>
        <Link to={link(`f/${f.id}`)} className="tree-link">
          <FolderIcon />
          <span className="ellipsis">{f.name}</span>
        </Link>
        {drop.state === "drop-ok" && <span className="drop-hint">Move here</span>}
        {drop.state === "drop-no" && (
          <>
            <span className="drop-hint">Can’t move</span>
            <span role="tooltip" className="drop-tip">
              {f.name} already has a stack named “{drag?.name}”. Rename one of them first.
            </span>
          </>
        )}
        {!drop.state && (
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
            {tree && folderContents(tree, f.id).empty && (
              <button className="icon-btn tip danger-icon" data-tip={`Delete ${f.name} (empty folder)`} aria-label={`Delete ${f.name}`}
                onClick={() => open({ kind: "delete-folder", folder: f })}>
                <TrashIcon />
              </button>
            )}
          </span>
        )}
      </div>
      {isOpen && <TreeLevel parentID={f.id} depth={depth + 1} query={query} path={path} />}
    </div>
  );
}

function OrgSwitcher({ onClose }: { onClose: () => void }) {
  const { org } = useOrg();
  const { me } = useSession();
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
      {(me.installation_admin || (orgs.data?.length ?? 0) > 5) && (
        <label className="search small-search">
          <SearchIcon size={13} />
          <input type="search" placeholder="Find organization" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
        </label>
      )}
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
          {me.user && me.installation_admin && me.organizations.some((m) => m.id === o.id) && <span className="muted small">Member</span>}
          {o.id === org.id && <span aria-hidden>✓</span>}
        </button>
      ))}
      {me.installation_admin && (
        <>
          <div className="menu-sep" />
          <button role="menuitem" className="menu-item" onClick={() => navigate("/new-org")}>
            <PlusIcon /> Create organization
          </button>
        </>
      )}
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
  const failed = active.filter((r) => r.status === "failed");
  const groups: { title: string; runs: ActiveRun[] }[] = [
    { title: "Needs approval", runs: active.filter((r) => r.status === "awaiting_approval") },
    { title: "Running", runs: active.filter((r) => r.status === "planning" || r.status === "applying") },
    { title: "Queued", runs: active.filter((r) => r.status === "queued" || r.status === "apply_queued") },
    { title: "Failed in the last hour", runs: failed },
  ].filter((g) => g.runs.length > 0);

  return (
    <div className="drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="drawer" aria-label="Active runs">
        <header className="drawer-header">
          <h2>
            Active runs <span className="muted">· {active.length - failed.length}</span>
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
                      {r.type} · {r.commit_author_name ? `${r.commit_author_name} · ` : ""}started by {runOriginText(r)}
                    </span>
                  </span>
                  <span className="queue-side">
                    <StatusBadge status={r.status} run={r} />
                    <span className="muted small">{timeAgo(r.status === "failed" && r.finished_at ? r.finished_at : r.created_at)}</span>
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
