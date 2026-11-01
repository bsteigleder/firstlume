import { useEffect, useState } from "react";
import type { Declaration, VaultCheckResult } from "../api";
import { useOrg } from "../org";
import { ErrorBanner, useAction } from "../ui";

/** The Vault paths a declaration reads, and those that depend on the folders a pattern matches. */
export function declaredVaultPaths(d?: Declaration): { fixed: string[]; templated: string[] } {
  const refs = [
    ...(d?.files ?? []).flatMap((f) => f.refs ?? []),
    ...(d?.env ?? []).map((v) => v.ref ?? ""),
    ...(d?.terraform ?? []).map((v) => v.ref ?? ""),
    ...(d?.registries ?? []).flatMap((r) => r.refs ?? []),
  ];
  const paths = [...new Set(refs.filter((r) => r.startsWith("vault:")).map((r) => r.slice("vault:".length).replace(/#[^#]*$/, "")))];
  return { fixed: paths.filter((p) => !p.includes("${")), templated: paths.filter((p) => p.includes("${")) };
}

/**
 * Logs in as a stack would and asks Vault whether its role can read each
 * path. Nothing is read.
 */
export function VaultAccessCheck({ stackID, initialPaths, auto }: { stackID: string; initialPaths: string[]; auto?: boolean }) {
  const { api } = useOrg();
  const [phase, setPhase] = useState<"plan" | "apply">("plan");
  const [text, setText] = useState(initialPaths.join("\n"));
  const [result, setResult] = useState<VaultCheckResult | null>(null);
  const check = useAction();
  const joined = initialPaths.join("\n");
  useEffect(() => setText(joined), [joined, stackID]);

  async function run(paths = text) {
    setResult(null);
    await check.run(async () => {
      const list = paths.split("\n").map((p) => p.trim()).filter(Boolean);
      setResult(await api.vaultCheck(stackID, phase, list));
    });
  }
  // The stack's own tab checks what it declares as soon as it opens.
  useEffect(() => {
    if (auto && joined) void run(joined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, stackID, joined]);

  const denied = result?.paths?.filter((p) => !p.can_read).length ?? 0;
  return (
    <div className="vault-check">
      <div className="grid-2">
        <label className="field span-2">
          Paths
          <textarea className="mono" rows={Math.min(Math.max(text.split("\n").length, 2), 8)} value={text} onChange={(e) => setText(e.target.value)}
            placeholder="secret/infra/dev/n01" spellCheck={false} />
        </label>
        <label className="field">
          Phase
          <select value={phase} onChange={(e) => setPhase(e.target.value as "plan" | "apply")}>
            <option value="plan">plan (and drift)</option>
            <option value="apply">apply</option>
          </select>
        </label>
      </div>
      <div className="form-actions start">
        <button className="primary" disabled={check.busy || !text.trim()} onClick={() => run()}>
          {check.busy ? "Checking…" : "Check access"}
        </button>
      </div>
      {check.error && <ErrorBanner error={check.error} onDismiss={() => check.setError(null)} />}
      {result?.error && <p className="notice notice-danger">{result.error}</p>}
      {result && !result.error && (
        <>
          {denied > 0 && (
            <p className="notice notice-danger">
              {denied} of {result.paths?.length} paths can’t be read with the role {result.role}: a run that needs them fails in Prepare.
            </p>
          )}
          <ul className="check-list">
            {result.paths?.map((p) => (
              <li key={p.path}>
                <span className={`badge ${p.error ? "warn" : p.can_read ? "ok" : "bad"}`}>{p.error ? "Error" : p.can_read ? "Can read" : "Can’t read"}</span>
                <span className="mono">{p.path}</span>
                {p.error && <span className="muted small">{p.error}</span>}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
