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

export interface Organization {
  id: string;
  slug: string;
  name: string;
  created_at: string;
}

export interface Folder {
  id: string;
  parent_id?: string;
  name: string;
  /** Ancestor ids from the top level down, ending with this folder's own. */
  path: string[];
  created_at: string;
  updated_at: string;
}

export interface StackConfig {
  version: number;
  engine: string;
  engine_version: string;
  terragrunt_version?: string;
  run_all?: boolean;
}

export interface Drift {
  status: "in_sync" | "drifted" | "error";
  run_id?: string;
  checked_at: string;
}

export interface Stack {
  id: string;
  folder_id?: string;
  name: string;
  repository: string;
  ref: string;
  path: string;
  has_git_token: boolean;
  config?: StackConfig;
  drift?: Drift;
  created_at: string;
  updated_at: string;
}

export interface StackSummary extends Stack {
  last_run?: Run;
}

export interface Tree {
  folders: Folder[];
  stacks: StackSummary[];
}

export interface CreateStackRequest {
  folder_id: string;
  name: string;
  repository: string;
  ref: string;
  path: string;
  git_token: string;
}

export type UpdateStackRequest = Partial<Pick<CreateStackRequest, "name" | "repository" | "ref" | "path" | "git_token">>;

export type VariableCategory = "env" | "terraform";

export interface Variable {
  key: string;
  category: VariableCategory;
  sensitive: boolean;
  value?: string;
}

export type LevelKind = "organization" | "folder" | "stack" | "default";

export interface Level {
  kind: LevelKind;
  id?: string;
  name?: string;
}

export interface LevelSettings {
  require_approval: boolean | null;
  drift_interval_minutes: number | null;
  confirm_destructive: boolean | null;
  variables: Variable[];
}

export interface EffectiveVariable extends Variable {
  origin: Level;
  overrides?: Level;
}

export interface EffectiveSettings {
  require_approval: { value: boolean; origin: Level };
  drift_interval_minutes: { value: number; origin: Level };
  confirm_destructive: { value: boolean; origin: Level };
  variables: EffectiveVariable[];
}

export interface SettingsResponse {
  level: LevelSettings;
  effective: EffectiveSettings;
}

export interface PlanSummary {
  add: number;
  change: number;
  replace: number;
  destroy: number;
}

export type ChangeAction = "create" | "update" | "replace" | "delete";

export interface AttributeChange {
  path: string;
  before?: string;
  after?: string;
  forces_replacement?: boolean;
}

export interface ResourceChange {
  unit?: string;
  address: string;
  type: string;
  name: string;
  action: ChangeAction;
  attributes?: AttributeChange[];
  truncated?: boolean;
}

/** Identifies a resource across a run --all plan (what approvals acknowledge). */
export function changeKey(c: ResourceChange): string {
  return c.unit ? `${c.unit}:${c.address}` : c.address;
}

export interface RunSettings {
  require_approval: { value: boolean; origin: Level };
  confirm_destructive: { value: boolean; origin: Level };
  drift_interval_minutes: { value: number; origin: Level };
  variables: EffectiveVariable[];
}

export interface Run {
  id: string;
  stack_id: string;
  type: RunType;
  status: RunStatus;
  ref: string;
  commit_sha?: string;
  commit_author_name?: string;
  commit_author_email?: string;
  commit_message?: string;
  has_changes?: boolean;
  plan_summary?: PlanSummary;
  error?: string;
  agent_id?: string;
  created_by: string;
  approved_by?: string;
  acknowledged?: string[];
  config?: StackConfig;
  settings_used?: RunSettings;
  approved_at?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ActiveRun extends Run {
  stack_name: string;
  folder_path: string[];
}

export type StepStatus = "running" | "succeeded" | "failed" | "skipped";

export interface RunStep {
  phase: Phase;
  name: string;
  kind: "core" | "hook";
  status: StepStatus;
  started_at?: string;
  finished_at?: string;
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

/** Settings live at three kinds of level; this builds the right path for each. */
export function settingsPath(level: { kind: "organization" } | { kind: "folder" | "stack"; id: string }): string {
  if (level.kind === "organization") return "/settings";
  return `/${level.kind === "folder" ? "folders" : "stacks"}/${level.id}/settings`;
}

export const api = {
  // Used by the login screen to check a token before storing it.
  checkToken: (token: string) => request<Organization[]>("GET", "/orgs", undefined, token),

  listOrgs: () => request<Organization[]>("GET", "/orgs"),
  createOrg: (slug: string, name: string) => request<Organization>("POST", "/orgs", { slug, name }),

  /** Calls scoped to one organization (by slug). */
  org(slug: string) {
    const p = `/orgs/${encodeURIComponent(slug)}`;
    return {
      get: () => request<Organization>("GET", p),
      rename: (name: string) => request<Organization>("PATCH", p, { name }),
      tree: () => request<Tree>("GET", `${p}/tree`),

      createFolder: (parentID: string, name: string) => request<Folder>("POST", `${p}/folders`, { parent_id: parentID, name }),
      renameFolder: (id: string, name: string) => request<Folder>("PATCH", `${p}/folders/${id}`, { name }),
      deleteFolder: (id: string) => request<void>("DELETE", `${p}/folders/${id}`),

      getSettings: (levelPath: string) => request<SettingsResponse>("GET", `${p}${levelPath}`),
      putSettings: (levelPath: string, s: LevelSettings) => request<SettingsResponse>("PUT", `${p}${levelPath}`, s),

      createStack: (req: CreateStackRequest) => request<Stack>("POST", `${p}/stacks`, req),
      getStack: (id: string) => request<Stack>("GET", `${p}/stacks/${id}`),
      updateStack: (id: string, req: UpdateStackRequest) => request<Stack>("PATCH", `${p}/stacks/${id}`, req),
      deleteStack: (id: string) => request<void>("DELETE", `${p}/stacks/${id}`),
      listRuns: (stackID: string, limit = 50) => request<Run[]>("GET", `${p}/stacks/${stackID}/runs?limit=${limit}`),
      createRun: (stackID: string, type: RunType, ref = "") => request<Run>("POST", `${p}/stacks/${stackID}/runs`, { type, ref }),

      activeRuns: () => request<ActiveRun[]>("GET", `${p}/runs?active=true`),
      getRun: (id: string) => request<Run>("GET", `${p}/runs/${id}`),
      changes: (id: string) => request<ResourceChange[]>("GET", `${p}/runs/${id}/changes`),
      steps: (id: string) => request<RunStep[]>("GET", `${p}/runs/${id}/steps`),
      approveRun: (id: string, acknowledged: string[]) => request<Run>("POST", `${p}/runs/${id}/approve`, { acknowledged }),
      discardRun: (id: string) => request<Run>("POST", `${p}/runs/${id}/discard`),
      cancelRun: (id: string) => request<Run>("POST", `${p}/runs/${id}/cancel`),
      logsURL: (id: string) => `/api/v1${p}/runs/${id}/logs`,

      listAgents: () => request<Agent[]>("GET", `${p}/agents`),
      createAgent: (name: string) => request<CreateAgentResponse>("POST", `${p}/agents`, { name }),
      deleteAgent: (id: string) => request<void>("DELETE", `${p}/agents/${id}`),
    };
  },
};

export type OrgAPI = ReturnType<typeof api.org>;

export interface LogEvent {
  phase: Phase;
  step: string;
  data: string;
}

export interface LogStreamHandlers {
  onLog: (e: LogEvent) => void;
  onEnd: (status: RunStatus) => void;
}

/**
 * Streams a run's logs over Server-Sent Events, starting after `cursor`.
 * EventSource cannot send an Authorization header, so this reads the stream
 * with fetch. Resolves with the last event id once the server ends the stream.
 */
export async function streamLogs(url: string, cursor: string, h: LogStreamHandlers, signal: AbortSignal): Promise<string> {
  const res = await fetch(`${url}?follow=true`, {
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
      if (event === "log") h.onLog(payload as LogEvent);
      else if (event === "end") h.onEnd(payload.status);
    }
  }
}
