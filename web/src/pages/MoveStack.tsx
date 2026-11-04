import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type RefObject, type DragEvent, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useNavigate } from "react-router";
import type { Folder, Level, MoveChange, MovePreview, Stack, Tree } from "../api";
import { FolderIcon, GripIcon, SearchIcon } from "../icons";
import { useOrg } from "../org";
import { driftLabel, ErrorBanner, Modal, StatusBadge, timeAgo, useAction } from "../ui";

// Moving a stack to another folder (canvas boards 23 and 24): drag its grip
// onto a folder in the tree, or click the grip for a folder list. Either way
// the move is reviewed before it happens, since what the stack inherits changes.

/** Why a stack can't be dropped on a folder ("" is the top level), or null when it can. */
export function dropRefusal(tree: Tree | null, stack: Stack, folderID: string): "same" | "taken" | null {
  if ((stack.folder_id ?? "") === folderID) return "same";
  const taken = tree?.stacks.some(
    (s) => s.id !== stack.id && (s.folder_id ?? "") === folderID && s.name.toLowerCase() === stack.name.toLowerCase(),
  );
  return taken ? "taken" : null;
}

/**
 * Drop handlers for a folder row (folderID "" for the organization). A folder
 * that refuses the stack says why; a closed one opens after half a second.
 */
export function useStackDrop(folderID: string, onHoverOpen?: () => void) {
  const { tree, drag, endDrag, open } = useOrg();
  const [over, setOver] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const refusal = drag ? dropRefusal(tree, drag, folderID) : null;

  useEffect(() => {
    if (!drag) setOver(false);
  }, [drag]);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const leave = () => {
    setOver(false);
    window.clearTimeout(timer.current);
  };
  return {
    state: !drag || !over || refusal === "same" ? "" : refusal === "taken" ? "drop-no" : "drop-ok",
    refusal,
    handlers: {
      onDragOver: (e: DragEvent) => {
        if (!drag) return;
        if (!over) {
          setOver(true);
          if (onHoverOpen) timer.current = window.setTimeout(onHoverOpen, 500);
        }
        if (!refusal) {
          e.preventDefault();
          e.dataTransfer.dropEffect = "move";
        }
      },
      onDragLeave: (e: DragEvent) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) leave();
      },
      onDrop: (e: DragEvent) => {
        e.preventDefault();
        leave();
        if (drag && !refusal) open({ kind: "move-stack", stack: drag, folderID });
        endDrag();
      },
    },
  };
}

/**
 * The grip of a stack row: drag it onto a folder, or click it (or Enter) for
 * the folder list. The row it sits in is what the pointer carries.
 */
export function StackGrip({ stack, rowSelector }: { stack: Stack; rowSelector: string }) {
  const { startDrag, endDrag } = useOrg();
  const [menu, setMenu] = useState(false);
  const anchor = useRef<HTMLButtonElement>(null);
  return (
    <span className="grip-wrap">
      <button
        ref={anchor}
        type="button"
        className="grip"
        draggable
        aria-label={`Move ${stack.name}`}
        aria-haspopup="dialog"
        aria-expanded={menu}
        title="Drag to a folder, or click to pick one"
        onClick={() => setMenu((m) => !m)}
        onDragStart={(e) => {
          e.dataTransfer.effectAllowed = "move";
          e.dataTransfer.setData("text/plain", stack.name);
          const row = e.currentTarget.closest(rowSelector);
          if (row) e.dataTransfer.setDragImage(row, 12, 14);
          setMenu(false);
          startDrag(stack);
        }}
        onDragEnd={endDrag}
      >
        <GripIcon />
      </button>
      {menu && <MoveMenu stack={stack} anchor={anchor} onClose={() => setMenu(false)} />}
    </span>
  );
}

interface Option {
  id: string;
  label: string;
  depth: number;
  note: string;
  disabled: boolean;
}

/** Folders in tree order, the top level first. */
function folderOptions(tree: Tree, orgName: string, stack: Stack): Option[] {
  const out: Option[] = [];
  const add = (id: string, label: string, depth: number) => {
    const r = dropRefusal(tree, stack, id);
    out.push({ id, label, depth, disabled: !!r, note: r === "same" ? "current" : r === "taken" ? `has ${stack.name}` : "" });
  };
  add("", `Top level of ${orgName}`, 0);
  const walk = (parent: string, depth: number) => {
    const kids = tree.folders
      .filter((f: Folder) => (f.parent_id ?? "") === parent)
      .sort((a, b) => a.name.localeCompare(b.name));
    for (const f of kids) {
      add(f.id, f.name, depth);
      walk(f.id, depth + 1);
    }
  };
  walk("", 0);
  return out;
}

/** The folder list a grip or the stack page's Move… button opens. */
export function MoveMenu({ stack, anchor, onClose, align = "left" }: {
  stack: Stack;
  anchor: RefObject<HTMLElement | null>;
  onClose: () => void;
  align?: "left" | "right";
}) {
  const { tree, org, open, folderLabel } = useOrg();
  const [q, setQ] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  const options = useMemo(() => (tree ? folderOptions(tree, org.name, stack) : []), [tree, org.name, stack]);
  const shown = q
    ? options.filter((o) => (o.id ? folderLabel(o.id) : o.label).toLowerCase().includes(q.toLowerCase()))
    : options;
  const enabled = shown.filter((o) => !o.disabled);
  const [active, setActive] = useState<string | null>(null);
  const current = enabled.find((o) => o.id === active) ?? enabled[0];

  // Fixed under its button, so the sidebar's scrolling box doesn't cut it off.
  const [pos, setPos] = useState<CSSProperties>({ left: -9999, top: 0 });
  useLayoutEffect(() => {
    const place = () => {
      const a = anchor.current?.getBoundingClientRect();
      const m = ref.current?.getBoundingClientRect();
      if (!a || !m) return;
      const left = Math.max(8, Math.min(align === "right" ? a.right - m.width : a.left, window.innerWidth - m.width - 8));
      const below = a.bottom + 4 + m.height <= window.innerHeight - 8;
      setPos({ left, top: below ? a.bottom + 4 : Math.max(8, a.top - 4 - m.height) });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [anchor, align]);

  useEffect(() => {
    const close = (e: MouseEvent) => !ref.current?.parentElement?.contains(e.target as Node) && onClose();
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [onClose]);

  const pick = (o: Option) => {
    onClose();
    open({ kind: "move-stack", stack, folderID: o.id });
  };
  const onKey = (e: ReactKeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const i = current ? enabled.indexOf(current) : -1;
      const next = enabled[(i + (e.key === "ArrowDown" ? 1 : enabled.length - 1)) % enabled.length];
      if (next) setActive(next.id);
    } else if (e.key === "Enter" && current) {
      e.preventDefault();
      pick(current);
    }
  };

  return (
    <div ref={ref} role="dialog" aria-label={`Move ${stack.name} to`} className="menu move-menu" style={pos} onKeyDown={onKey}>
      <strong className="move-menu-title">Move {stack.name} to…</strong>
      <label className="search small-search">
        <SearchIcon size={13} />
        <input type="search" placeholder="Find a folder" aria-label="Find a folder" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
      </label>
      <div role="listbox" aria-label="Folders" className="move-menu-list">
        {shown.map((o) => (
          <button
            key={o.id || "top"}
            type="button"
            role="option"
            aria-selected={o === current}
            aria-disabled={o.disabled}
            disabled={o.disabled}
            className={`menu-item move-option${o === current ? " current" : ""}`}
            style={{ paddingLeft: 9 + (q ? 0 : o.depth * 14) }}
            onMouseEnter={() => !o.disabled && setActive(o.id)}
            onClick={() => pick(o)}
          >
            <FolderIcon size={14} />
            <span className="ellipsis grow">{q && o.id ? folderLabel(o.id) : o.label}</span>
            {o.note && <span className="muted small">{o.note}</span>}
          </button>
        ))}
        {shown.length === 0 && <p className="muted small move-menu-empty">No folder matches.</p>}
      </div>
      <p className="muted small move-menu-help">↑ ↓ to pick · Enter to review · Esc to close</p>
    </div>
  );
}

function originName(l: Level | undefined, orgName: string): string {
  if (!l) return "";
  if (l.kind === "default") return "the default";
  if (l.kind === "organization") return l.name || orgName;
  if (l.kind === "stack") return "this stack";
  return l.name ?? "a folder";
}

const settingLabels: Record<string, string> = {
  require_approval: "Approval before apply",
  drift_interval_minutes: "Drift check",
  confirm_destructive: "Confirm each delete or replace",
  vault_role: "Vault role",
  git_on_push: "On push",
};

const categoryLabels: Record<string, string> = {
  env: "Environment variable",
  terraform: "Terraform variable",
  file: "File",
};

function changeLabel(c: MoveChange): string {
  if (c.setting === "variable") return c.sensitive ? "Secret" : categoryLabels[c.category ?? ""] ?? "Variable";
  if (c.setting === "registry") return "Registry credential";
  return settingLabels[c.setting] ?? c.setting;
}

function changeValue(c: MoveChange, v: MoveChange["before"]): string {
  if (!v) return "Not defined";
  switch (c.setting) {
    case "require_approval":
      return v.value === "true" ? "Required" : "Not required";
    case "confirm_destructive":
      return v.value === "true" ? "On" : "Off";
    case "drift_interval_minutes":
      return driftLabel(Number(v.value));
    case "git_on_push":
      return { off: "Off", plan: "Plan", apply: "Apply" }[v.value ?? "off"] ?? v.value ?? "";
    case "registry":
      return v.value ? `user ${v.value}` : "set";
  }
  if (c.sensitive) return "••••••";
  if (c.category === "file") {
    const lines = (v.value ?? "").split("\n").filter(Boolean).length;
    return `${lines} line${lines === 1 ? "" : "s"}`;
  }
  return v.value ?? "";
}

const mono = (c: MoveChange) => c.setting === "vault_role" || (c.setting === "variable" && !c.sensitive && c.category !== "file");

/** Reviews what moving the stack changes, then moves it. */
export function MoveStackDialog({ stack, folderID, onClose }: { stack: Stack; folderID: string; onClose: () => void }) {
  const { api, org, folderLabel, folderChain, reloadTree, notify, link } = useOrg();
  const [preview, setPreview] = useState<MovePreview | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const { busy, error, run } = useAction();
  const navigate = useNavigate();
  const from = stack.folder_id ?? "";
  const where = (id: string) => (id ? folderLabel(id) : `the top level of ${org.name}`);
  const toName = folderID ? folderChain(folderID).at(-1)?.name ?? "the folder" : org.name;

  useEffect(() => {
    api.previewMove(stack.id, folderID).then(setPreview, (e: Error) => setLoadError(e.message));
  }, [api, stack.id, folderID]);

  const move = () =>
    run(async () => {
      await api.moveStack(stack.id, folderID);
      await reloadTree();
      onClose();
      notify(
        <>
          Moved <strong>{stack.name}</strong> to {where(folderID)}.
        </>,
        async () => {
          await api.moveStack(stack.id, from);
          await reloadTree();
        },
      );
    });

  if (preview?.active_run) {
    const r = preview.active_run;
    return (
      <Modal title={`Can’t move “${stack.name}” now`} onClose={onClose}>
        <div className="modal-body">
          <p className="notice notice-attention">
            It has a run <StatusBadge status={r.status} /> started {timeAgo(r.created_at)}.{" "}
            {r.status === "awaiting_approval"
              ? `Its plan used the settings of ${where(from)}: approve or discard it, then move the stack.`
              : "Wait for it to finish, then move the stack."}
          </p>
          <div className="form-actions">
            <button type="button" onClick={onClose}>
              Close
            </button>
            <button
              type="button"
              onClick={() => {
                onClose();
                navigate(link(`r/${r.id}`));
              }}
            >
              Open run
            </button>
          </div>
        </div>
      </Modal>
    );
  }

  const changes = preview?.changes ?? [];
  const approvalOff = changes.some((c) => c.setting === "require_approval" && c.after?.value === "false");
  const missing = changes.filter((c) => (c.setting === "variable" || c.setting === "registry" || c.setting === "vault_role") && c.before && !c.after);
  const own = [
    ...(preview?.own_variables ?? []).map((v) => v.key),
    ...(preview?.own_settings ?? []).map((s) => (settingLabels[s] ?? s).toLowerCase()),
  ];

  return (
    <Modal title={`Move “${stack.name}” to ${folderID ? folderLabel(folderID) : `the top level of ${org.name}`}?`} onClose={onClose} wide>
      <div className="modal-body move-review">
        <p className="muted">
          Now in <strong className="ink">{where(from)}</strong>. After the move it inherits from <strong className="ink">{toName}</strong> instead.
        </p>
        <ErrorBanner error={loadError} />
        {!preview && !loadError && <p className="muted">Comparing settings…</p>}
        {preview?.name_taken && (
          <p className="notice notice-danger">
            {toName} already has a stack named “{stack.name}”. Rename one of them first.
          </p>
        )}
        {preview && !preview.name_taken && (approvalOff || missing.length > 0) && (
          <p className="notice notice-attention">
            {approvalOff && "Applies will no longer wait for approval. "}
            {missing.length > 0 &&
              `${missing.length} ${missing.length === 1 ? "setting the stack gets today is" : "settings the stack gets today are"} not defined there: the next run fails if the code needs ${missing.length === 1 ? "it" : "them"}.`}
          </p>
        )}

        {preview && changes.length === 0 && (
          <p className="notice notice-success">No inherited setting changes: {toName} gives this stack the same settings it has today.</p>
        )}
        {changes.length > 0 && (
          <section>
            <h3 className="move-h">
              What changes · {changes.length} inherited setting{changes.length === 1 ? "" : "s"}
            </h3>
            <div className="table-box">
              <table className="table move-table">
                <thead>
                  <tr>
                    <th>Setting</th>
                    <th>Now</th>
                    <th>After the move</th>
                  </tr>
                </thead>
                <tbody>
                  {changes.map((c, i) => (
                    <tr key={i}>
                      <td>
                        <div>{changeLabel(c)}</div>
                        {c.key && <div className="mono muted small">{c.key}</div>}
                      </td>
                      <td>
                        <div className={mono(c) && c.before ? "mono" : ""}>{changeValue(c, c.before)}</div>
                        {c.before && <div className="muted small">from {originName(c.before.origin, org.name)}</div>}
                      </td>
                      <td>
                        <div className={`move-after ${mono(c) && c.after ? "mono" : ""} ${!c.after && c.before ? "danger-text" : ""}`}>{changeValue(c, c.after)}</div>
                        {c.after ? (
                          <div className="muted small">from {originName(c.after.origin, org.name)}</div>
                        ) : (
                          c.before && c.setting !== "require_approval" && <div className="small danger-text">Missing after the move</div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        )}
        {own.length > 0 && (
          <p className="hint">
            Set on the stack itself, so they go with it and still win: <span className="mono">{own.join(", ")}</span>. Secret values are never shown.
          </p>
        )}

        <section>
          <h3 className="move-h">What stays the same</h3>
          <ul className="move-same">
            <li>Runs, logs and approvals history.</li>
            <li>The state and every version of it. New versions are saved under the new folder’s names; older ones stay readable where they are.</li>
            <li>State tokens, drift history and the repository source.</li>
          </ul>
        </section>

        {error && <div className="form-error">{error}</div>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={!preview || preview.name_taken || busy} onClick={move} autoFocus>
            Move stack
          </button>
        </div>
      </div>
    </Modal>
  );
}

/** Opens the folder list under the stack page's Move… button. */
export function MoveButton({ stack }: { stack: Stack }) {
  const [menu, setMenu] = useState(false);
  const anchor = useRef<HTMLButtonElement>(null);
  return (
    <span className="grip-wrap">
      <button ref={anchor} type="button" aria-haspopup="dialog" aria-expanded={menu} onClick={() => setMenu((m) => !m)}>
        Move…
      </button>
      {menu && <MoveMenu stack={stack} anchor={anchor} align="right" onClose={() => setMenu(false)} />}
    </span>
  );
}
