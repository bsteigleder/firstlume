import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { settingsPath, type CommandCheck, type RunType, type Stack, type StateAccess } from "../api";
import { useOrg } from "../org";
import { commandFirstLine, Modal, useAction, useData } from "../ui";

const kinds: { type: RunType; title: string; text: string; danger?: boolean }[] = [
  { type: "apply", title: "Plan & apply", text: "Plans, waits for approval if required, then applies that plan." },
  { type: "plan", title: "Plan only", text: "See what would change. Never applies." },
  { type: "drift", title: "Check drift", text: "Compare the real infrastructure with the code. Never applies." },
  {
    type: "command",
    title: "Run a command",
    text: "Terraform state, import, output and a few more, in the stack’s folder, with its variables and state.",
  },
  { type: "destroy", title: "Destroy", text: "Plans deleting everything this stack manages. Always shows the plan first.", danger: true },
];

/** What "Run again" or a shortcut presets. */
export interface RunPreset {
  type: RunType;
  ref: string;
  command?: string;
  state_access?: StateAccess;
}

/** Starts a run by hand: what to run, on which branch or commit. `again` presets them from an earlier run. */
export function RunDialog({ stack, again, onClose }: { stack: Stack; again?: RunPreset; onClose: () => void }) {
  const { api, link, reloadActive } = useOrg();
  const navigate = useNavigate();
  const [type, setType] = useState<RunType>(again?.type ?? "apply");
  const [ref, setRef] = useState(again?.ref || stack.ref);
  const [command, setCommand] = useState(again?.command ?? "");
  const [access, setAccess] = useState<StateAccess>(again?.state_access ?? "read");
  const { busy, error, run } = useAction();
  const isCommand = type === "command";
  const check = useCommandCheck(stack.id, isCommand ? command : "", access);
  const settings = useData(() => api.getSettings(settingsPath({ kind: "stack", id: stack.id })), [stack.id]);
  const approval = !!settings.data?.effective.require_approval.value;
  const asksApproval = isCommand && access === "write" && approval;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (type === "destroy" && !confirm(`Plan destroying everything “${stack.name}” manages? You will review the plan before anything is deleted.`)) return;
    await run(async () => {
      const r = await api.createRun(
        stack.id,
        type,
        ref.trim() === stack.ref ? "" : ref.trim(),
        isCommand ? { command, state_access: access } : undefined,
      );
      void reloadActive();
      onClose();
      navigate(link(`r/${r.id}`));
    });
  }

  return (
    <Modal title={`Run ${stack.name}`} onClose={onClose} wide={isCommand}>
      <form className="modal-body" onSubmit={submit}>
        <fieldset className="choices">
          <legend>What to run</legend>
          {kinds.map((k) => (
            <label key={k.type} className={`choice${type === k.type ? " selected" : ""}${k.danger ? " danger-choice" : ""}`}>
              <input type="radio" name="kind" checked={type === k.type} onChange={() => setType(k.type)} />
              <span>
                <strong>{k.title}</strong>
                <span className="hint">{k.text}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {isCommand && (
          <CommandFields stack={stack} command={command} setCommand={setCommand} access={access} setAccess={setAccess} check={check} approval={approval} />
        )}
        <label className="field">
          Branch or commit
          <input className="mono" value={ref} onChange={(e) => setRef(e.target.value)} required />
          <span className="hint">The stack follows {stack.ref}. Any branch, tag or commit SHA of the repository works.</span>
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="form-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className={type === "destroy" ? "danger" : "primary"} disabled={busy || (isCommand && !check?.ok)}>
            {busy ? "Starting…" : asksApproval ? "Ask for approval" : "Start run"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/** Checks the command as it is typed, a moment after the last key (spec 0009, board 70). */
function useCommandCheck(stackID: string, command: string, access: StateAccess): CommandCheck | null {
  const { api } = useOrg();
  const [result, setResult] = useState<{ key: string; check: CommandCheck } | null>(null);
  const key = `${access}\n${command}`;
  useEffect(() => {
    if (!command.trim()) return;
    let live = true;
    const t = setTimeout(() => {
      api.checkCommand(stackID, command, access).then(
        (check) => live && setResult({ key, check }),
        () => live && setResult(null),
      );
    }, 250);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [api, stackID, command, access, key]);
  if (!command.trim()) return null;
  return result?.key === key ? result.check : null;
}

function CommandFields({
  stack,
  command,
  setCommand,
  access,
  setAccess,
  check,
  approval,
}: {
  stack: Stack;
  command: string;
  setCommand: (c: string) => void;
  access: StateAccess;
  setAccess: (a: StateAccess) => void;
  check: CommandCheck | null;
  approval: boolean;
}) {
  const { api } = useOrg();
  const runs = useData(() => api.listRuns(stack.id, 50), [stack.id]);
  // The last distinct commands of this stack, newest first (R22).
  const recent = useMemo(() => {
    const seen = new Set<string>();
    const out: { command: string; access: StateAccess }[] = [];
    for (const r of runs.data ?? []) {
      if (r.type !== "command" || !r.command || seen.has(r.command)) continue;
      seen.add(r.command);
      out.push({ command: r.command, access: r.state_access ?? "read" });
      if (out.length === 5) break;
    }
    return out;
  }, [runs.data]);
  const engine = stack.config?.engine === "opentofu" ? "tofu" : "terraform";
  const refused = check && !check.ok ? check.error : undefined;

  return (
    <>
      <div className="field">
        <label htmlFor="command-input">Command</label>
        <textarea
          id="command-input"
          className={`mono command-input${refused ? " invalid" : ""}`}
          rows={3}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          placeholder={`${engine} state list`}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          aria-invalid={!!refused}
          aria-describedby="command-check command-hint"
          required
        />
        <span id="command-check" aria-live="polite" className={`command-check${refused ? " bad" : check?.ok ? " ok" : ""}`}>
          {refused
            ? refused.message
            : check?.ok
              ? `✓ ${check.lines} ${check.lines === 1 ? "command" : "commands"}${check.writes ? " · changes the state" : ""}`
              : ""}
        </span>
        <span className="hint" id="command-hint">
          Runs in <span className="mono">{stack.path || "."}</span>, without a shell: one command per line, in order, stopping at the first error. No
          pipes, no variables, no other programs. Don’t paste secrets: the command is kept with the run.
        </span>
        <details className="command-help hint">
          <summary>What is accepted</summary>
          <ul>
            <li>
              <span className="mono">state list</span>, <span className="mono">state show</span>, <span className="mono">output</span>,{" "}
              <span className="mono">show</span>, <span className="mono">validate</span>, <span className="mono">providers</span>,{" "}
              <span className="mono">version</span>, <span className="mono">workspace list</span>/<span className="mono">show</span>: read only.
            </li>
            <li>
              <span className="mono">state mv</span>, <span className="mono">state rm</span>, <span className="mono">state replace-provider</span>,{" "}
              <span className="mono">import</span>, <span className="mono">taint</span>, <span className="mono">untaint</span>,{" "}
              <span className="mono">force-unlock</span>: change the state.
            </li>
            {stack.config?.terragrunt_version ? (
              <li>
                Start with <span className="mono">terragrunt</span>, which sets up each unit’s state: <span className="mono">terragrunt run --all -- state list</span>, or
                for a unit <span className="mono">terragrunt --working-dir units/vpc state list</span>.
              </li>
            ) : (
              <li>
                Start with <span className="mono">{engine}</span>.
              </li>
            )}
            <li>
              Not here: <span className="mono">apply</span>, <span className="mono">destroy</span>, <span className="mono">plan</span>,{" "}
              <span className="mono">state pull</span>/<span className="mono">push</span>, <span className="mono">-json</span> on output and show, and any other
              program, such as ssh or curl.
            </li>
          </ul>
        </details>
      </div>
      {recent.length > 0 && (
        <div className="field">
          <span className="hint">Used before in this stack</span>
          <div className="command-chips">
            {recent.map((c) => (
              <button
                type="button"
                key={c.command}
                className="command-chip mono"
                title={c.command}
                onClick={() => {
                  setCommand(c.command);
                  setAccess(c.access);
                }}
              >
                {commandFirstLine(c.command)}
                {commandCount(c.command) > 1 ? ` +${commandCount(c.command) - 1}` : ""}
              </button>
            ))}
          </div>
        </div>
      )}
      <fieldset className="choices">
        <legend>State</legend>
        <label className={`choice${access === "read" ? " selected" : ""}`}>
          <input type="radio" name="access" checked={access === "read"} onChange={() => setAccess("read")} />
          <span>
            <strong>Read only</strong>
            <span className="hint">State list and show, output, show, validate. Commands that change the state are refused before the run starts.</span>
          </span>
        </label>
        <label className={`choice${access === "write" ? " selected" : ""}`}>
          <input type="radio" name="access" checked={access === "write"} onChange={() => setAccess("write")} />
          <span>
            <strong>May change the state</strong>
            <span className="hint">
              State mv and rm, import, taint, force-unlock. No other run of the stack starts while it runs.
              {approval ? " This stack requires approval, so someone approves the command before it runs." : ""}
            </span>
          </span>
        </label>
      </fieldset>
    </>
  );
}

/** How many commands a text holds: lines, joining those continued with "\". */
function commandCount(text: string): number {
  let n = 0;
  let continued = false;
  for (const l of text.split("\n")) {
    const t = l.trim();
    if (!continued && (t === "" || t.startsWith("#"))) continue;
    if (!continued) n++;
    continued = /\\\s*$/.test(l);
  }
  return n;
}
