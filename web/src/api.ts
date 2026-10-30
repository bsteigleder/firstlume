// Client for the control-plane API (/api/v1). Mirrors internal/api/types.go.

export type RunType = "plan" | "apply" | "destroy" | "drift";
export type Phase = "plan" | "apply";
export type RunStatus =
  | "queued"
  | "planning"
  | "awaiting_approval"
  | "apply_queued"
  | "applying"
  | "planned"
  | "no_changes"
  | "drifted"
  | "applied"
  | "failed"
  | "discarded"
  | "canceled";

export const terminalStatuses: RunStatus[] = ["planned", "no_changes", "drifted", "applied", "failed", "discarded", "canceled"];

export function isTerminal(s: RunStatus): boolean {
  return terminalStatuses.includes(s);
}

export interface Template {
  id: string;
  name: string;
  repository: string;
  path: string;
  default_ref: string;
  engine: "terraform" | "opentofu";
  engine_version: string;
  terragrunt_version?: string;
  has_git_token: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateTemplateRequest {
  name: string;
  repository: string;
  path: string;
  default_ref: string;
  engine: string;
  engine_version: string;
  terragrunt_version: string;
  git_token: string;
}

export interface Environment {
  id: string;
  template_id: string;
  name: string;
  ref?: string;
  auto_approve: boolean;
  /** 0 when scheduled drift detection is off. */
  drift_interval_minutes: number;
  drift?: Drift;
  created_at: string;
  updated_at: string;
}

export interface Drift {
  status: "in_sync" | "drifted" | "error";
  run_id?: string;
  checked_at: string;
}

export interface UpdateEnvironmentRequest {
  name?: string;
  ref?: string;
  auto_approve?: boolean;
  drift_interval_minutes?: number;
}

export interface CreateEnvironmentRequest {
  template_id: string;
  name: string;
  ref: string;
  auto_approve: boolean;
}

export type VariableCategory = "env" | "terraform";

export interface Variable {
  key: string;
  category: VariableCategory;
  sensitive: boolean;
  value?: string;
}

export interface PlanSummary {
  add: number;
  change: number;
  destroy: number;
}

export interface Run {
  id: string;
  environment_id: string;
  type: RunType;
  status: RunStatus;
  ref: string;
  commit_sha?: string;
  has_changes?: boolean;
  plan_summary?: PlanSummary;
  error?: string;
  agent_id?: string;
  created_by: string;
  approved_by?: string;
  approved_at?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
}

export interface Agent {
  id: string;
  name: string;
  last_seen_at?: string;
  created_at: string;
}

export interface CreateAgentResponse {
  agent: Agent;
  token: string;
}

// The admin token lives in sessionStorage: it survives reloads but not closing the tab.
const tokenKey = "firstlume.token";

export function getToken(): string | null {
  try {
    return sessionStorage.getItem(tokenKey);
  } catch {
    return null;
  }
}

export function setToken(token: string | null) {
  try {
    if (token) sessionStorage.setItem(tokenKey, token);
    else sessionStorage.removeItem(tokenKey);
  } catch {
    // storage unavailable: the user will be asked again on reload
  }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

// Called when the API rejects the token, so the app can return to the login screen.
let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

async function request<T>(method: string, path: string, body?: unknown, token = getToken()): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token ?? ""}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    onUnauthorized();
    throw new ApiError(401, "invalid or missing token");
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      // non-JSON error body
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

export const api = {
  // Used by the login screen to check a token before storing it.
  checkToken: (token: string) => request<Template[]>("GET", "/templates", undefined, token),

  listTemplates: () => request<Template[]>("GET", "/templates"),
  createTemplate: (req: CreateTemplateRequest) => request<Template>("POST", "/templates", req),
  deleteTemplate: (id: string) => request<void>("DELETE", `/templates/${id}`),

  listEnvironments: () => request<Environment[]>("GET", "/environments"),
  getEnvironment: (id: string) => request<Environment>("GET", `/environments/${id}`),
  createEnvironment: (req: CreateEnvironmentRequest) => request<Environment>("POST", "/environments", req),
  updateEnvironment: (id: string, req: UpdateEnvironmentRequest) => request<Environment>("PATCH", `/environments/${id}`, req),
  deleteEnvironment: (id: string) => request<void>("DELETE", `/environments/${id}`),
  listVariables: (id: string) => request<Variable[]>("GET", `/environments/${id}/variables`),
  putVariables: (id: string, variables: Variable[]) =>
    request<Variable[]>("PUT", `/environments/${id}/variables`, { variables }),
  listRuns: (envID: string, limit = 50) => request<Run[]>("GET", `/environments/${envID}/runs?limit=${limit}`),
  createRun: (envID: string, type: RunType, ref = "") => request<Run>("POST", `/environments/${envID}/runs`, { type, ref }),

  getRun: (id: string) => request<Run>("GET", `/runs/${id}`),
  approveRun: (id: string) => request<Run>("POST", `/runs/${id}/approve`),
  discardRun: (id: string) => request<Run>("POST", `/runs/${id}/discard`),
  cancelRun: (id: string) => request<Run>("POST", `/runs/${id}/cancel`),

  listAgents: () => request<Agent[]>("GET", "/agents"),
  createAgent: (name: string) => request<CreateAgentResponse>("POST", "/agents", { name }),
  deleteAgent: (id: string) => request<void>("DELETE", `/agents/${id}`),
};

export interface LogStreamHandlers {
  onLog: (phase: Phase, data: string) => void;
  onEnd: (status: RunStatus) => void;
}

/**
 * Streams a run's logs over Server-Sent Events, starting after `cursor`.
 * EventSource cannot send an Authorization header, so this reads the stream
 * with fetch. Resolves with the last event id once the server ends the stream.
 */
export async function streamLogs(runID: string, cursor: string, h: LogStreamHandlers, signal: AbortSignal): Promise<string> {
  const res = await fetch(`/api/v1/runs/${runID}/logs?follow=true`, {
    headers: {
      Authorization: `Bearer ${getToken() ?? ""}`,
      Accept: "text/event-stream",
      ...(cursor ? { "Last-Event-ID": cursor } : {}),
    },
    signal,
  });
  if (res.status === 401) {
    onUnauthorized();
    throw new ApiError(401, "invalid or missing token");
  }
  if (!res.ok || !res.body) throw new ApiError(res.status, `log stream failed: ${res.statusText}`);

  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  let last = cursor;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return last;
    buf += value;
    let sep: number;
    while ((sep = buf.indexOf("\n\n")) >= 0) {
      const raw = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      let event = "message";
      let id = "";
      const data: string[] = [];
      for (const line of raw.split("\n")) {
        if (line.startsWith(":")) continue;
        const i = line.indexOf(":");
        const field = i < 0 ? line : line.slice(0, i);
        const val = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
        if (field === "event") event = val;
        else if (field === "id") id = val;
        else if (field === "data") data.push(val);
      }
      if (data.length === 0) continue;
      const payload = JSON.parse(data.join("\n"));
      if (id) last = id;
      if (event === "log") h.onLog(payload.phase, payload.data);
      else if (event === "end") h.onEnd(payload.status);
    }
  }
}
