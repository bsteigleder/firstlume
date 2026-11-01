import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { ApiError, settingsPath, type Folder, type GitConnection, type Stack } from "../api";
import { useOrg } from "../org";
import { Modal, useAction, useData } from "../ui";
import { NewStackWizard } from "./NewStackWizard";

/** Creates a folder (parentID) or renames one (folder). */
export function FolderNameDialog({ parentID, folder, onClose }: { parentID?: string; folder?: Folder; onClose: () => void }) {
  const { api, reloadTree, folderLabel, folderChain, link } = useOrg();
  const navigate = useNavigate();
  const [name, setName] = useState(folder?.name ?? "");
  const { busy, error, run } = useAction();
  const depth = parentID ? folderChain(parentID).length + 1 : 1;

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      if (folder) {
        await api.renameFolder(folder.id, name);
        await reloadTree();
      } else {
        const f = await api.createFolder(parentID ?? "", name);
        await reloadTree();
        navigate(link(`f/${f.id}`));
      }
      onClose();
    });
  }

  return (
    <Modal title={folder ? "Rename folder" : "New folder"} onClose={onClose}>
      <form className="modal-body" onSubmit={submit}>
        {!folder && (
          <p className="muted">
            {parentID ? (
              <>
                Inside <strong className="ink">{folderLabel(parentID)}</strong> (level {depth} of 10).
              </>
            ) : (
              "At the top level of the organization."
            )}
          </p>
        )}
        <label className="field">
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus required aria-invalid={!!error} />
        </label>
        {error && <div className="form-error">{error}</div>}
        {!folder && <p className="hint">Stacks you put here inherit the settings and secrets above it unless the folder sets its own.</p>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !name.trim()}>
            {folder ? "Rename" : "Create folder"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/** Renames a stack; everything else about it stays as it is. */
export function RenameStackDialog({ stack, onClose, onSaved }: { stack: Stack; onClose: () => void; onSaved: () => Promise<void> }) {
  const { api, reloadTree, folderChain, folderLabel } = useOrg();
  const [name, setName] = useState(stack.name);
  const { busy, error, run } = useAction();
  const storage = useData(() => api.getStateStorage(), [api]);
  const settings = useData(() => api.getSettings(settingsPath({ kind: "stack", id: stack.id })), [api, stack.id]);
  const parent = folderChain(stack.folder_id).at(-1)?.name;
  const where = stack.folder_id ? folderLabel(stack.folder_id) : "the top level";
  const unchanged = name.trim() === stack.name;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (unchanged) return onClose();
    const ok = await run(async () => {
      try {
        await api.updateStack(stack.id, { name });
      } catch (err) {
        if (err instanceof ApiError && err.status === 409)
          throw new Error(`“${name.trim()}” already exists in ${where}. Names are unique per folder, ignoring case.`);
        throw err;
      }
      await Promise.all([reloadTree(), onSaved()]);
    });
    if (ok) onClose();
  }

  return (
    <Modal title="Rename stack" onClose={onClose}>
      <form className="modal-body" onSubmit={submit}>
        <label className="field">
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus required aria-invalid={!!error} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <p className="muted">Runs, state, settings, variables and tokens stay with the stack, and its link doesn’t change.</p>
        {storage.data && storage.data.current.kind !== "installation" && (
          <p className="notice">
            New state versions are saved under <span className="mono">…/{parent ? `${parent}/` : ""}{name.trim() || "…"}/</span> in the organization’s
            storage. Earlier versions stay where they are and remain readable.
          </p>
        )}
        {settings.data?.effective.vault_role.value && (
          <p className="notice notice-attention">
            Runs’ Vault tokens carry the new name in <span className="mono">stack_name</span>. A Vault role bound to{" "}
            <span className="mono">stack_id</span> keeps working; one bound to the name needs updating.
          </p>
        )}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !name.trim()}>
            Rename
          </button>
        </div>
      </form>
    </Modal>
  );
}

export function DeleteFolderDialog({ folder, onClose }: { folder: Folder; onClose: () => void }) {
  const { api, tree, reloadTree, link } = useOrg();
  const navigate = useNavigate();
  const { busy, error, run } = useAction();
  const subfolders = tree?.folders.filter((f) => f.parent_id === folder.id).length ?? 0;
  const stacks = tree?.stacks.filter((s) => s.folder_id === folder.id).length ?? 0;
  const empty = subfolders === 0 && stacks === 0;

  return (
    <Modal title={`Delete “${folder.name}”?`} onClose={onClose}>
      <div className="modal-body">
        {empty ? (
          <p>Deleting an empty folder also deletes the settings and secrets defined on it.</p>
        ) : (
          <p className="notice notice-attention">
            Only empty folders can be deleted. <strong>{folder.name}</strong> still has {subfolders} folder{subfolders === 1 ? "" : "s"} and {stacks} stack
            {stacks === 1 ? "" : "s"}: move or delete them first.
          </p>
        )}
        {error && <div className="form-error">{error}</div>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            {empty ? "Cancel" : "Close"}
          </button>
          <button
            className="danger"
            disabled={!empty || busy}
            onClick={() =>
              run(async () => {
                await api.deleteFolder(folder.id);
                await reloadTree();
                navigate(folder.parent_id ? link(`f/${folder.parent_id}`) : link());
                onClose();
              })
            }
          >
            Delete folder
          </button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * New stack: from the organization's Git connection when there is one
 * (repository, branch and a discovered .firstlume folder), otherwise by typing
 * the repository URL and folder.
 */
export function NewStackDialog({ folderID, onClose }: { folderID: string; onClose: () => void }) {
  const { api } = useOrg();
  // undefined while loading, null when the organization has no connection.
  const [conn, setConn] = useState<GitConnection | null | undefined>(undefined);
  const [manual, setManual] = useState(false);
  useEffect(() => {
    api.gitConnection().then(setConn, () => setConn(null));
  }, [api]);
  if (conn === undefined) return null;
  return (
    <Modal title="New stack" onClose={onClose} wide>
      {conn && !manual ? (
        <div className="modal-body">
          <NewStackWizard folderID={folderID} conn={conn} onClose={onClose} onManual={() => setManual(true)} />
        </div>
      ) : (
        <ManualStack folderID={folderID} onClose={onClose} connected={!!conn} />
      )}
    </Modal>
  );
}

function ManualStack({ folderID, onClose, connected }: { folderID: string; onClose: () => void; connected: boolean }) {
  const { api, reloadTree, folderLabel, link } = useOrg();
  const navigate = useNavigate();
  const [form, setForm] = useState({ name: "", repository: "", ref: "main", path: "", git_token: "" });
  const { busy, error, run } = useAction();
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const suggested = form.path.split("/").filter(Boolean).pop();

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const st = await api.createStack({ folder_id: folderID, ...form, name: form.name || suggested || "" });
      await reloadTree();
      navigate(link(`s/${st.id}`));
      onClose();
    });
  }

  return (
    <form className="modal-body" onSubmit={submit}>
        <p className="muted">
          In <strong className="ink">{folderLabel(folderID)}</strong>. It will inherit the settings and secrets above it.
        </p>
        <div className="notice">
          {connected ? (
            "Type any repository URL. Stacks created this way clone with the token you give here, not the Git connection’s."
          ) : (
            <>
              This organization has no Git connection, so repositories and <span className="mono">.firstlume</span> folders can’t be listed. Type
              them instead, or <Link to={link("settings#git-connection")} onClick={onClose}>connect Gitea</Link>.
            </>
          )}
        </div>
        <div className="grid-2">
          <label className="field span-2">
            Repository URL
            <input className="mono" value={form.repository} onChange={set("repository")} placeholder="https://git.example.com/acme/infra.git" required autoFocus />
            <span className="hint">https://, ssh:// or git@host:owner/repo.git</span>
          </label>
          <label className="field">
            Branch
            <input className="mono" value={form.ref} onChange={set("ref")} />
          </label>
          <label className="field">
            Folder that contains <span className="mono">.firstlume</span>
            <input className="mono" value={form.path} onChange={set("path")} placeholder="environments/prod" />
            <span className="hint">
              Relative to the repository root; empty or <span className="mono">.</span> for the root.
            </span>
          </label>
          <label className="field">
            Stack name
            <input value={form.name} onChange={set("name")} placeholder={suggested || "prod"} />
          </label>
          <label className="field">
            Access token <span className="optional">only for private repositories</span>
            <input type="password" autoComplete="off" value={form.git_token} onChange={set("git_token")} placeholder="Read-only token" />
          </label>
        </div>
        {error && <div className="form-error">{error}</div>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !form.repository || !(form.name || suggested)}>
            {busy ? "Creating…" : "Create stack"}
          </button>
        </div>
    </form>
  );
}
