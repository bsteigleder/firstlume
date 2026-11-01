import type { Declaration, Stack } from "../api";
import { declaredVaultPaths, VaultAccessCheck } from "./VaultParts";

/** "vault:secret/x#ip" → a chip saying where a value comes from. */
function Ref({ refText }: { refText: string }) {
  const [kind, rest] = [refText.slice(0, refText.indexOf(":")), refText.slice(refText.indexOf(":") + 1)];
  return <span className={`ref mono${kind === "vault" ? " vault" : ""}`}>{kind === "vault" ? `vault ${rest}` : `var ${rest}`}</span>;
}

/**
 * What the stack's .firstlume/config.yaml declares, as read by the last plan
 * (spec 0003): files, variables and registries, where each value comes from,
 * and whether the stack's Vault role can read them. No values.
 */
export function StackDeclaration({ stack }: { stack: Stack }) {
  const d: Declaration | undefined = stack.config?.declaration;
  const configFile = `${stack.path ? `${stack.path}/` : ""}.firstlume/config.yaml`;
  const vaultPaths = declaredVaultPaths(d);
  const vars = [
    ...(d?.env ?? []).map((v) => ({ ...v, kind: "Env var" })),
    ...(d?.terraform ?? []).map((v) => ({ ...v, kind: "Terraform" })),
  ];

  return (
    <>
      <section className="card form-card">
        <div className="section-head">
          <h2>Files the runs write</h2>
          <span className="muted small">
            declared in <span className="mono">{configFile}</span>, as read by the last plan
          </span>
        </div>
        {!d?.files?.length ? (
          <p className="muted">
            None declared. A <span className="mono">files</span> list in the config writes files before init, from literals, variables or Vault, one per
            folder with a pattern like <span className="mono">sp*/*/connection.hcl</span>.
          </p>
        ) : (
          d.files.map((f) => (
            <article key={f.path} className="declared-file">
              <div className="head">
                <strong className="mono">{f.path}</strong>
                <span className="badge idle">{f.block ? `${f.format} · ${f.block}` : f.format}</span>
                {f.sensitive && <span className={`badge ${f.refs?.some((r) => r.startsWith("vault:")) ? "vault" : "warn"}`}>Secret</span>}
              </div>
              {/[*?[]/.test(f.path) && <span className="hint">One file in each folder that matches the pattern, at every run.</span>}
              <div className="ref-list">
                <span className="muted">Values from</span>
                {f.refs?.length ? f.refs.map((r) => <Ref key={r} refText={r} />) : <span className="muted">literals in the file</span>}
              </div>
            </article>
          ))
        )}
      </section>

      {(vars.length > 0 || !!d?.registries?.length) && (
        <section className="card form-card">
          <h2>Variables and registries the repository declares</h2>
          <div className="table-box">
            <table className="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Kind</th>
                  <th>Value from</th>
                </tr>
              </thead>
              <tbody>
                {vars.map((v) => (
                  <tr key={`${v.kind}/${v.name}`}>
                    <td className="mono">{v.name}</td>
                    <td>{v.kind}</td>
                    <td>{v.ref ? <Ref refText={v.ref} /> : <span className="muted">literal</span>}</td>
                  </tr>
                ))}
                {d?.registries?.map((r) => (
                  <tr key={`registry/${r.host}`}>
                    <td className="mono">{r.host}</td>
                    <td>Registry · {r.kind === "terraform" ? "Terraform" : "OCI"}</td>
                    <td className="ref-list">{r.refs?.length ? r.refs.map((x) => <Ref key={x} refText={x} />) : <span className="muted">literal</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="hint">The stack’s settings win over these for the same name or registry host; the run’s log says when.</p>
        </section>
      )}

      {(vaultPaths.fixed.length > 0 || vaultPaths.templated.length > 0) && (
        <section className="card form-card">
          <h2>Vault access</h2>
          <p className="muted">
            Checked as soon as this tab opens, for the paths the config reads. Values are never shown: each run reads them, masks them in the logs and
            fails the apply if one changed since its plan.
          </p>
          {vaultPaths.templated.length > 0 && (
            <p className="hint">
              Paths with <span className="mono">${"{name}"}</span> or <span className="mono">${"{dir}"}</span> depend on the folders and aren’t checked:{" "}
              {vaultPaths.templated.map((p) => <span key={p} className="mono">{p} </span>)}
            </p>
          )}
          <VaultAccessCheck stackID={stack.id} initialPaths={vaultPaths.fixed} auto />
        </section>
      )}
    </>
  );
}
