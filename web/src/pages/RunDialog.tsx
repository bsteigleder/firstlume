import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import type { RunType, Stack } from "../api";
import { useOrg } from "../org";
import { Modal, useAction } from "../ui";

const kinds: { type: RunType; title: string; text: string; danger?: boolean }[] = [
  { type: "apply", title: "Plan & apply", text: "Plans, waits for approval if required, then applies that plan." },
  { type: "plan", title: "Plan only", text: "See what would change. Never applies." },
  { type: "drift", title: "Check drift", text: "Compare the real infrastructure with the code. Never applies." },
  { type: "destroy", title: "Destroy", text: "Plans deleting everything this stack manages. Always shows the plan first.", danger: true },
];

/** Starts a run by hand: what to run, on which branch or commit. `again` presets them from an earlier run. */
export function RunDialog({ stack, again, onClose }: { stack: Stack; again?: { type: RunType; ref: string }; onClose: () => void }) {
  const { api, link, reloadActive } = useOrg();
  const navigate = useNavigate();
  const [type, setType] = useState<RunType>(again?.type ?? "apply");
  const [ref, setRef] = useState(again?.ref || stack.ref);
  const { busy, error, run } = useAction();

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (type === "destroy" && !confirm(`Plan destroying everything “${stack.name}” manages? You will review the plan before anything is deleted.`)) return;
    await run(async () => {
      const r = await api.createRun(stack.id, type, ref.trim() === stack.ref ? "" : ref.trim());
      void reloadActive();
      onClose();
      navigate(link(`r/${r.id}`));
    });
  }

  return (
    <Modal title={`Run ${stack.name}`} onClose={onClose}>
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
          <button className={type === "destroy" ? "danger" : "primary"} disabled={busy}>
            {busy ? "Starting…" : "Start run"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
