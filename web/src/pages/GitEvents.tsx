import { Fragment, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { settingsPath, type GitEvent, type GitEventOutcome, type GitEventStack, type Stack } from "../api";
import { ExternalIcon } from "../icons";
import { PageTitle, useCrumbs, useOrg } from "../org";
import { commitURL, Empty, ErrorBanner, linkProvider, timeAgo, useData } from "../ui";

// Why it ran, or didn't (spec 0006, R10; canvas board 58).

const outcomes: Record<GitEventOutcome, { label: string; tone: string }> = {
  run_created: { label: "Run started", tone: "ok" },
  not_affected: { label: "Not affected", tone: "idle" },
  trigger_off: { label: "Trigger off", tone: "idle" },
  other_branch: { label: "Other branch", tone: "idle" },
  duplicate: { label: "Already had a run", tone: "idle" },
  already_handled: { label: "Already handled", tone: "idle" },
  error: { label: "Error", tone: "bad" },
};

/** The provider's name for texts, from the organization's connection. */
function providerName(p: string | null) {
  return p === "gitlab" ? "GitLab" : "Gitea";
}

function affectsAllText(reason: string, provider: string | null): string {
  const name = providerName(provider);
  switch (reason) {
    case "force_push":
      return "Force push: no way to tell what changed, so every stack on the branch counts";
    case "new_branch":
      return "New branch: no way to tell what changed, so every stack on the branch counts";
    case "compare_failed":
      return `${name} couldn’t compare the commits, so every stack on the branch counts`;
    case "compare_incomplete":
      return provider === "gitlab"
        ? "GitLab listed 1000 changed files, its limit, so the list may be cut: every stack on the branch counts"
        : `${name} didn’t list every changed file, so every stack on the branch counts`;
  }
  return reason;
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

function eventSummary(e: GitEvent) {
  if (e.status === "failed") return <span className="badge bad">Error</span>;
  if (e.status !== "done") return <span className="badge run">Processing</span>;
  const count = (o: GitEventOutcome) => e.stacks.filter((d) => d.outcome === o).length;
  const runs = count("run_created");
  const rest = [
    [count("not_affected"), "not affected"],
    [count("trigger_off"), "trigger off"],
    [count("other_branch"), "other branch"],
  ].filter(([n]) => n) as [number, string][];
  return (
    <>
      {runs > 0 && <span className="badge ok">{plural(runs, "run")}</span>}
      {runs === 0 && count("duplicate") > 0 && <span className="badge idle">Already had a run</span>}
      {e.stacks.length === 0 && <span className="muted small">No stack on this repository</span>}
      {rest.length > 0 && <span className="muted small"> {rest.map(([n, w]) => `${n} ${w}`).join(" · ")}</span>}
    </>
  );
}

/** A list of the files a decision looked at, opened on request. */
function FilesToggle({ decision }: { decision: GitEventStack }) {
  const [open, setOpen] = useState(false);
  const files = decision.files ?? [];
  const total = decision.files_total || files.length;
  if (files.length === 0) return null;
  return (
    <>
      <button type="button" className="link-inline" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? "Hide files" : "Show files"}
      </button>
      {open && (
        <ul className="git-files mono small">
          {files.map((f) => (
            <li key={f}>{f}</li>
          ))}
          {total > files.length && <li className="muted">and {total - files.length} more</li>}
        </ul>
      )}
    </>
  );
}

function decisionWhy(d: GitEventStack, stackRef: string | undefined, provider: string | null, sha: string) {
  const total = d.files_total || d.files?.length || 0;
  switch (d.outcome) {
    case "run_created":
    case "duplicate":
      if (d.reason === "files_changed") {
        return d.files?.length ? <span className="mono small">{d.files[0]}{total > 1 ? ` and ${total - 1} more` : ""}</span> : "Its files changed";
      }
      if (d.reason === "config_unreadable") return "Its .firstlume/config.yaml couldn’t be read at this commit, so the run shows why";
      return d.reason ? affectsAllText(d.reason, provider) : null;
    case "not_affected":
      return `${plural(total, "file")} changed, none of them for this stack. `;
    case "trigger_off":
      return "On push is off";
    case "already_handled":
      return `Commit ${sha} was already handled for this stack. Nothing new to run (${providerName(provider)}’s test button sends this).`;
    case "other_branch":
      return stackRef ? `This stack follows ${stackRef}` : "This stack follows another branch";
    default:
      return d.reason ?? null;
  }
}

function Decisions({ event }: { event: GitEvent }) {
  const { link, tree, gitProvider } = useOrg();
  const refOf = (id?: string) => tree?.stacks.find((s) => s.id === id)?.ref;
  if (event.stacks.length === 0) return null;
  return (
    <div className="table-box">
      <table className="table">
        <thead>
          <tr>
            <th>Stack</th>
            <th>Decision</th>
            <th>Why</th>
            <th>Run</th>
          </tr>
        </thead>
        <tbody>
          {event.stacks.map((d) => (
            <tr key={d.stack_id ?? d.stack_name}>
              <td>{d.stack_id ? <Link to={link(`s/${d.stack_id}`)}>{d.stack_name}</Link> : d.stack_name}</td>
              <td>
                <span className={`badge ${outcomes[d.outcome].tone}`}>{outcomes[d.outcome].label}</span>
              </td>
              <td className="small">
                {decisionWhy(d, refOf(d.stack_id), gitProvider, event.after_sha.slice(0, 7))}
                {d.outcome === "not_affected" && <FilesToggle decision={d} />}
                {d.outcome === "trigger_off" && d.stack_id && (
                  <>
                    . <Link to={link(`s/${d.stack_id}/settings`)}>Settings</Link>
                  </>
                )}
              </td>
              <td>{d.run_id ? <Link className="mono" to={link(`r/${d.run_id}`)}>{d.run_id.slice(0, 8)}</Link> : <span className="muted">—</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function GitEventsPage() {
  const { api, org, link, tree } = useOrg();
  const [params, setParams] = useSearchParams();
  const stack = params.get("stack") ?? "";
  const repository = params.get("repository") ?? "";
  const events = useData(() => api.gitEvents(stack), [api, stack], { poll: true, pollMs: 5000 });
  // The event asked for in the address starts open; otherwise the newest does.
  const [opened, setOpened] = useState<string | null | undefined>(params.get("event") ?? undefined);
  useCrumbs([{ label: org.name, to: link() }, { label: "Settings", to: link("settings") }, { label: "Git events" }]);

  const setFilter = (key: string, value: string) =>
    setParams((p) => {
      if (value) p.set(key, value);
      else p.delete(key);
      p.delete("event");
      return p;
    });

  if (events.error && !events.data) return <ErrorBanner error={events.error} />;
  if (!events.data) return null;
  const repositories = [...new Set(events.data.map((e) => e.repository))].sort();
  const list = events.data.filter((e) => !repository || e.repository === repository);
  const open = opened === undefined ? list[0]?.id : opened;

  return (
    <div className="page">
      <PageTitle title="Git events" sub={<span className="muted">Every push received and the decision for each stack. Kept for 30 days.</span>} />
      <div className="filters">
        <label className="filter">
          Repository
          <select value={repository} onChange={(e) => setFilter("repository", e.target.value)}>
            <option value="">All</option>
            {repositories.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
        <label className="filter">
          Stack
          <select value={stack} onChange={(e) => setFilter("stack", e.target.value)}>
            <option value="">All</option>
            {tree?.stacks.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
      </div>

      {list.length === 0 ? (
        <Empty>
          No Git events yet. Once a repository’s webhook points to First Lume, pushes show up here.{" "}
          <Link to={link("settings#git-connection")}>Git connection</Link>
        </Empty>
      ) : (
        <div className="table-box">
          <table className="table git-events">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>By</th>
                <th>Result</th>
              </tr>
            </thead>
            <tbody>
              {list.map((e) => (
                <EventRow key={e.id} event={e} open={open === e.id} onToggle={() => setOpened(open === e.id ? null : e.id)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function EventRow({ event: e, open, onToggle }: { event: GitEvent; open: boolean; onToggle: () => void }) {
  const { tree, gitProvider } = useOrg();
  // Any stack of the repository gives the Git address of the commit.
  const st = tree?.stacks.find((s) => s.repository.toLowerCase().includes(e.repository.toLowerCase()));
  const url = st ? commitURL(st.repository, e.after_sha, linkProvider(st, gitProvider)) : null;
  const sha = e.after_sha.slice(0, 7);
  return (
    <Fragment>
      <tr className={open ? "git-event open" : "git-event"}>
        <td className="muted small">{timeAgo(e.received_at)}</td>
        <td>
          <button type="button" className="link-inline row-title" aria-expanded={open} onClick={onToggle}>
            Push to <span className="mono">{e.ref}</span>
          </button>
          <div className="muted small">
            <span className="mono">{e.repository}</span> ·{" "}
            {url ? (
              <a className="mono" href={url} target="_blank" rel="noreferrer">
                {sha} <ExternalIcon size={11} />
              </a>
            ) : (
              <span className="mono">{sha}</span>
            )}{" "}
            · {plural(e.total_commits, "commit")}
            {e.affects_all && <div>{affectsAllText(e.affects_all, gitProvider)}</div>}
            {e.status === "failed" && e.error && <div className="danger-text">Could not process it: {e.error}</div>}
          </div>
        </td>
        <td className="small">{e.sender}</td>
        <td>{eventSummary(e)}</td>
      </tr>
      {open && e.stacks.length > 0 && (
        <tr className="git-event-detail">
          <td colSpan={4}>
            <Decisions event={e} />
          </td>
        </tr>
      )}
    </Fragment>
  );
}

/**
 * The answer to "why didn't it run?" on the stack: one line, only when its last
 * push on the branch didn't start a run, or a trigger is on and nothing came.
 */
export function GitLastDecision({ stack }: { stack: Stack }) {
  const { api, link, gitProvider } = useOrg();
  const events = useData(() => api.gitEvents(stack.id, 20), [api, stack.id], { poll: true, pollMs: 10000 });
  const settings = useData(() => api.getSettings(settingsPath({ kind: "stack", id: stack.id })), [api, stack.id]);
  if (!events.data) return null;
  if (events.data.length === 0) return settings.data && settings.data.effective.git_on_push.value !== "off" ? <GitNothingBanner /> : null;

  // Pushes to other branches say nothing about this stack.
  for (const e of events.data) {
    const d = e.stacks.find((x) => x.stack_id === stack.id);
    if (!d || d.outcome === "other_branch") continue;
    if (d.outcome !== "not_affected" && d.outcome !== "trigger_off") return null;
    const sha = e.after_sha.slice(0, 7);
    const url = commitURL(stack.repository, e.after_sha, linkProvider(stack, gitProvider));
    const commit = url ? (
      <a className="mono" href={url} target="_blank" rel="noreferrer">
        {sha} <ExternalIcon size={11} />
      </a>
    ) : (
      <span className="mono">{sha}</span>
    );
    return (
      <div className="git-last card">
        <span className="badge idle">{outcomes[d.outcome].label}</span>
        <span className="grow">
          {d.outcome === "not_affected" ? (
            <>
              Last push {commit} by {e.sender} changed {plural(d.files_total || d.files?.length || 0, "file")}, none of them for this stack. Files outside its
              path count only if declared under <span className="mono">triggers</span> in <span className="mono">.firstlume/config.yaml</span>.{" "}
              <FilesToggle decision={d} />
            </>
          ) : (
            <>
              Last push {commit} by {e.sender} started nothing: On push is off. <Link to={link(`s/${stack.id}/settings`)}>Settings</Link>
            </>
          )}
        </span>
        <Link to={link(`git-events?stack=${stack.id}`)}>All events</Link>
      </div>
    );
  }
  return null;
}

function GitNothingBanner() {
  const { link, gitProvider } = useOrg();
  return (
    <div className="git-last card">
      <span className="badge warn">Nothing received</span>
      <span className="grow">
        On push is on, and this repository hasn’t sent First Lume anything yet. Check its webhook in {providerName(gitProvider)}.
      </span>
      <Link to={link("git-events")}>Git events</Link>
    </div>
  );
}

/** Under the setting: a trigger is on and no event ever came (canvas board 56). */
export function GitNothingReceived({ stackID }: { stackID?: string }) {
  const { api, link, gitProvider } = useOrg();
  const events = useData(() => api.gitEvents(stackID ?? "", 1), [api, stackID]);
  if (!events.data || events.data.length > 0) return null;
  return (
    <p className="notice notice-attention">
      <strong>Nothing received {stackID ? "from this repository" : "from any repository"} yet.</strong> Check the webhook in {providerName(gitProvider)}.{" "}
      <Link to={link("git-events")}>Git events</Link>
    </p>
  );
}
