import { useState, type FormEvent } from "react";
import type { RegistryCredential } from "../api";
import { Modal } from "../ui";

/** A file variable being added or edited: its content is only sent when changed. */
export interface FileDraft {
  key: string;
  value: string;
  sensitive: boolean;
  // The content is stored and was not sent back: empty keeps it.
  stored?: boolean;
}

const maxFileBytes = 256 << 10;

export function FileDialog({ draft, levelName, onSave, onClose }: {
  draft?: FileDraft;
  levelName: string;
  onSave: (f: FileDraft) => void;
  onClose: () => void;
}) {
  const [key, setKey] = useState(draft?.key ?? "");
  const [value, setValue] = useState(draft?.value ?? "");
  const [sensitive, setSensitive] = useState(draft?.sensitive ?? false);
  const keepsStored = !!draft?.stored && sensitive && value === "";
  const tooBig = new TextEncoder().encode(value).length > maxFileBytes;

  function submit(e: FormEvent) {
    e.preventDefault();
    onSave({ key: key.trim(), value, sensitive, stored: keepsStored });
  }

  return (
    <Modal title={`${draft ? "Edit" : "Add"} file · ${levelName}`} onClose={onClose} wide>
      <form className="modal-body form-card" onSubmit={submit}>
        <p className="muted">Inherited by every stack below, like the variables.</p>
        <label className="field">
          Path
          <input className="mono" value={key} onChange={(e) => setKey(e.target.value)} placeholder="certs/ca.pem" required autoFocus />
          <span className="hint">
            Relative to the stack’s folder. Never inside .git or .firstlume, and never over a file the repository has: the run fails instead.
          </span>
        </label>
        <label className="field">
          Content
          <textarea className="mono" rows={10} spellCheck={false} value={value} onChange={(e) => setValue(e.target.value)}
            placeholder={draft?.stored ? "•••••• (saved, never shown: paste to replace)" : "Paste the file here"} />
          {tooBig ? <span className="hint warn-text">Larger than 256 KiB.</span> : <span className="hint">Up to 256 KiB of text.</span>}
        </label>
        <label className="check">
          <input type="checkbox" checked={sensitive} onChange={(e) => setSensitive(e.target.checked)} />
          <span>
            <strong>Secret</strong>
            <span className="hint block">Written with permission 0600, masked in the logs line by line, never shown again after saving.</span>
          </span>
        </label>
        <p className="notice">
          Need the content from Vault, or one file per machine folder? Declare it in the repository’s <span className="mono">.firstlume/config.yaml</span>.
        </p>
        <div className="form-actions">
          <button type="button" onClick={onClose}>Cancel</button>
          <button className="primary" disabled={!key.trim() || tooBig || (!value && !keepsStored)}>
            {draft ? "Done" : "Add file"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/** A registry credential being added or edited at one level. */
export interface RegistryDraft extends RegistryCredential {
  // The level already stores a typed password for this host: empty keeps it.
  storedPassword?: boolean;
}

export function RegistryDialog({ draft, levelName, role, onSave, onClose }: {
  draft?: RegistryDraft;
  levelName: string;
  role: string;
  onSave: (r: RegistryDraft) => void;
  onClose: () => void;
}) {
  const [host, setHost] = useState(draft?.host ?? "");
  const [kind, setKind] = useState<"oci" | "terraform">(draft?.kind ?? "oci");
  const [username, setUsername] = useState(draft?.username ?? "");
  const [fromVault, setFromVault] = useState(!!draft?.password_vault);
  const [password, setPassword] = useState("");
  const [vaultPath, setVaultPath] = useState(draft?.password_vault?.path ?? "");
  const [vaultField, setVaultField] = useState(draft?.password_vault?.field ?? "");
  const keepsPassword = !fromVault && !!draft?.storedPassword && password === "";
  const ready = host.trim() && (kind === "terraform" || username.trim()) && (fromVault ? vaultPath.trim() && vaultField.trim() : password || keepsPassword);

  function submit(e: FormEvent) {
    e.preventDefault();
    onSave({
      host: host.trim().toLowerCase(),
      kind,
      username: kind === "oci" ? username.trim() : undefined,
      password: fromVault ? undefined : password || undefined,
      password_vault: fromVault ? { path: vaultPath.trim(), field: vaultField.trim() } : undefined,
      storedPassword: keepsPassword,
    });
  }

  return (
    <Modal title={`Registry credential · ${levelName}`} onClose={onClose} wide>
      <form className="modal-body form-card" onSubmit={submit}>
        <p className="muted">Overrides the credential for the same host from the levels above.</p>
        <label className="field">
          Host
          <input className="mono" value={host} onChange={(e) => setHost(e.target.value)} placeholder="harbor.example.com" required autoFocus />
          <span className="hint">Host and optional port, without https://.</span>
        </label>
        <fieldset className="field plain-fieldset">
          <legend>Kind</legend>
          <label className="check">
            <input type="radio" name="kind" checked={kind === "oci"} onChange={() => setKind("oci")} />
            <span>
              OCI registry<span className="hint block">Harbor, GHCR, Docker Hub: charts and images, through Docker’s config.json.</span>
            </span>
          </label>
          <label className="check">
            <input type="radio" name="kind" checked={kind === "terraform"} onChange={() => setKind("terraform")} />
            <span>
              Terraform registry<span className="hint block">Private modules and providers, through TF_TOKEN_&lt;host&gt; (token only).</span>
            </span>
          </label>
        </fieldset>
        {kind === "oci" && (
          <label className="field">
            Username
            <input className="mono" value={username} onChange={(e) => setUsername(e.target.value)} required />
          </label>
        )}
        <fieldset className="field plain-fieldset">
          <legend>{kind === "oci" ? "Password or token" : "Token"}</legend>
          <div role="radiogroup" aria-label="Where it comes from" className="segmented fit">
            <button type="button" role="radio" aria-checked={!fromVault} onClick={() => setFromVault(false)}>Type it</button>
            <button type="button" role="radio" aria-checked={fromVault} onClick={() => setFromVault(true)}>From Vault</button>
          </div>
          {fromVault ? (
            <>
              <div className="grid-vault-ref">
                <label className="field">
                  Vault path
                  <input className="mono" value={vaultPath} onChange={(e) => setVaultPath(e.target.value)} placeholder="secret/ci/harbor" />
                </label>
                <label className="field">
                  Field
                  <input className="mono" value={vaultField} onChange={(e) => setVaultField(e.target.value)} placeholder="token" />
                </label>
              </div>
              <span className="hint">
                Read by each run with the stack’s Vault role{role ? ` (${role} here)` : ""}. First Lume never stores it.
              </span>
            </>
          ) : (
            <input type="password" autoComplete="off" aria-label="Password" value={password} onChange={(e) => setPassword(e.target.value)}
              placeholder={draft?.storedPassword ? "•••••••• (saved, never shown)" : ""} />
          )}
        </fieldset>
        <div className="form-actions">
          <button type="button" onClick={onClose}>Cancel</button>
          <button className="primary" disabled={!ready}>{draft ? "Done" : "Add credential"}</button>
        </div>
      </form>
    </Modal>
  );
}
