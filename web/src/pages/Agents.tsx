import { useState, type FormEvent } from "react";
import { api, type CreateAgentResponse } from "../api";
import { Empty, ErrorBanner, PageHeader, timeAgo, useAction, useData } from "../ui";

// An agent polls every few seconds; one not seen for this long is probably down.
const onlineWindowMs = 60_000;

export function AgentsPage() {
  const { data, error, reload } = useData(api.listAgents, [], { poll: true, pollMs: 10000 });
  const [name, setName] = useState("");
  const [created, setCreated] = useState<CreateAgentResponse | null>(null);
  const action = useAction();

  async function create(e: FormEvent) {
    e.preventDefault();
    await action.run(async () => {
      setCreated(await api.createAgent(name));
      setName("");
      await reload();
    });
  }

  async function remove(id: string, agentName: string) {
    if (!confirm(`Delete agent "${agentName}"? It will stop picking up runs.`)) return;
    if (await action.run(() => api.deleteAgent(id))) await reload();
  }

  return (
    <>
      <PageHeader title="Agents" subtitle="Agents pull runs from Firstlume and execute them in their own cluster or network." />
      <ErrorBanner error={error ?? action.error} onDismiss={action.error ? () => action.setError(null) : undefined} />

      {created && (
        <div className="card token-card">
          <strong>Agent "{created.agent.name}" created.</strong>
          <p className="muted">Copy its token now. It is shown only once.</p>
          <div className="token-row">
            <code>{created.token}</code>
            <button onClick={() => void navigator.clipboard?.writeText(created.token)}>Copy</button>
          </div>
          <p className="muted">
            Install it with Helm: <code>--set agent.token=…</code>, or locally with <code>FIRSTLUME_AGENT_TOKEN</code>.
          </p>
          <div className="form-actions">
            <button onClick={() => setCreated(null)}>Done</button>
          </div>
        </div>
      )}

      <form className="inline-form" onSubmit={create}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="new agent name, e.g. prod-cluster" />
        <button className="primary" disabled={!name || action.busy}>
          Create agent
        </button>
      </form>

      {data &&
        (data.length === 0 ? (
          <Empty>No agents registered.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Last seen</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((a) => {
                const online = a.last_seen_at && Date.now() - new Date(a.last_seen_at).getTime() < onlineWindowMs;
                return (
                  <tr key={a.id}>
                    <td>
                      <strong>{a.name}</strong>
                    </td>
                    <td>
                      <span className={`badge ${online ? "badge-success" : "badge-neutral"}`}>{online ? "Online" : "Offline"}</span>
                    </td>
                    <td className="muted">{timeAgo(a.last_seen_at)}</td>
                    <td className="muted">{timeAgo(a.created_at)}</td>
                    <td className="right">
                      <button className="link" onClick={() => remove(a.id, a.name)} disabled={action.busy}>
                        Delete
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ))}
    </>
  );
}
