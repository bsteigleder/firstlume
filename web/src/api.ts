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
  declaration?: Declaration;
}

/** What .firstlume/config.yaml declares a run needs (spec 0003), without values.
 * A ref is "var:NAME" or "vault:path#field"; a literal has none. */
export interface Declaration {
  files?: { path: string; format: string; block?: string; sensitive: boolean; refs?: string[] }[];
  env?: { name: string; ref?: string }[];
  terraform?: { name: string; ref?: string }[];
  registries?: { host: string; kind: string; refs?: string[] }[];
}

export interface VaultRef {
  path: string;
  field: string;
}

export interface RegistryCredential {
  host: string;
  kind?: "oci" | "terraform";
  username?: string;
  password?: string;
  password_vault?: VaultRef;
  has_password?: boolean;
}

export interface EffectiveRegistry extends RegistryCredential {
  origin: Level;
  overrides?: Level;
}

export interface VaultConnection {
  address: string;
  ca_cert?: string;
  namespace?: string;
  auth_path: string;
  audience: string;
  created_at: string;
  updated_at: string;
}

export interface VaultCheckResult {
  role: string;
  paths?: { path: string; can_read: boolean; error?: string }[];
  error?: string;
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
  /** Set when the stack clones through the organization's Git connection. */
  connection_id?: string;
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
  /** "owner/name" of a repository of the organization's Git connection. */
  git_repository?: string;
  repository: string;
  ref: string;
  path: string;
  git_token: string;
}

/** Where one uploaded state goes, or why that is left to a person (spec 0002 R9.9). */
export interface IdentifiedState {
  source: string;
  resources: number;
  serial: number;
  lineage: string;
  placed: boolean;
  unit: string;
  reason?: "lineage" | "path" | "resources" | "values" | "only_unit";
  matched: number;
  total: number;
  why?: "tie" | "no_match" | "contradicts" | "older_copy" | "competing";
  candidates?: string[];
  path_unit: string;
  already_in?: { stack_id: string; stack: string; unit: string }[];
}

export interface StateImportProblem {
  path: string;
  error: string;
}

export interface StateIdentifyResult {
  units: string[];
  code_note?: string;
  states: IdentifiedState[];
  ignored?: string[];
  problems?: StateImportProblem[];
}

export interface StatePlacement {
  source: string;
  unit: string;
  skip?: boolean;
}

export interface StateImportUnit {
  key: string;
  serial: number;
  lineage: string;
  resources: number;
  status: "new" | "same" | "newer" | "older" | "conflict";
  current_version?: number;
  current_serial?: number;
  action: "create" | "replace" | "skip";
  locked_by?: string;
  version?: number;
}

export interface StateImportResult {
  dry_run: boolean;
  units: StateImportUnit[];
}

export interface GitConnection {
  provider: string;
  url: string;
  account: string;
  account_kind: "organization" | "user";
  created_at: string;
  updated_at: string;
  repositories?: number;
}

export interface GitRepo {
  full_name: string;
  owner: string;
  name: string;
  default_branch: string;
  private: boolean;
  empty: boolean;
  web_url: string;
}

export interface DiscoveredStack {
  path: string;
  config?: StackConfig;
  error?: string;
  used_by?: { id: string; name: string; folder_id?: string };
}

export interface DiscoverResponse {
  repository: string;
  ref: string;
  commit: string;
  stacks: DiscoveredStack[];
}

/** What moving a stack to a folder would change in what it inherits. */
export interface MovePreview {
  folder_id?: string;
  /** A run queued, planning, awaiting approval or applying blocks the move. */
  active_run?: Run;
  /** The folder already has a stack with this name. */
  name_taken?: boolean;
  changes: MoveChange[];
  /** What the stack sets itself: it goes with it and still wins. */
  own_settings: string[];
  own_variables: Variable[];
}

export interface MoveChange {
  setting: "require_approval" | "drift_interval_minutes" | "confirm_destructive" | "vault_role" | "variable" | "registry";
  category?: VariableCategory;
  key?: string;
  sensitive?: boolean;
  /** Left out when not defined there. Secret values are never included. */
  before?: { value?: string; origin: Level };
  after?: { value?: string; origin: Level };
}

export type UpdateStackRequest = Partial<Pick<CreateStackRequest, "name" | "repository" | "ref" | "path" | "git_token">>;

export type VariableCategory = "env" | "terraform" | "file";

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
  vault_role?: string | null;
  variables: Variable[];
  /** Left out, the level's credentials stay as they are. */
  registries?: RegistryCredential[];
}

export interface EffectiveVariable extends Variable {
  origin: Level;
  overrides?: Level;
}

export interface EffectiveSettings {
  require_approval: { value: boolean; origin: Level };
  drift_interval_minutes: { value: number; origin: Level };
  confirm_destructive: { value: boolean; origin: Level };
  vault_role: { value: string; origin: Level };
  variables: EffectiveVariable[];
  registries: EffectiveRegistry[];
}

/** Where an organization's state versions are written; "installation" is the installation's own storage. */
export interface StateStorageSettings {
  current: { kind: "installation" | "s3"; bucket?: string; folder?: string };
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
  /** Which resource it is: what it will be, or for a delete what it was. */
  label?: ResourceLabel;
  attributes?: AttributeChange[];
  truncated?: boolean;
}

/** Identifies a resource across a run --all plan (what approvals acknowledge). */
export function changeKey(c: ResourceChange): string {
  return c.unit ? `${c.unit}:${c.address}` : c.address;
}

export interface StateLock {
  id: string;
  holder: string;
  operation?: string;
  run_id?: string;
  since: string;
}

export interface StateUnit {
  key: string;
  version: number;
  serial: number;
  resources: number;
  size: number;
  updated_at?: string;
  written_by?: string;
  run_id?: string;
  lock?: StateLock;
}

/** An apply that changed a resource. */
export interface ResourceEvent {
  action: ChangeAction;
  run_id: string;
  run_type: RunType;
  at: string;
  by: string;
}

/** One resource instance in a state: no attribute values. */
export interface StateResource {
  address: string;
  mode: "managed" | "data";
  type: string;
  name: string;
  module?: string;
  provider: string;
  instances: number;
  depends_on?: string[];
  /** Which resource it is, from a fixed list of identifying attributes per type. */
  label?: ResourceLabel;
  history?: ResourceEvent[];
  /** What the latest drift check would do to it to match the code. */
  drift?: ChangeAction;
}

export interface ResourceLabel {
  name: string;
  record?: DNSRecord;
  details?: { key: string; value: string }[];
}

export interface DNSRecord {
  type?: string;
  target?: string;
  proxied?: boolean;
  /** Seconds; 1 means automatic. */
  ttl?: number;
  zone?: string;
}

export interface ResourceUnit {
  key: string;
  version: number;
  serial: number;
  updated_at?: string;
  written_by?: string;
  run_id?: string;
  lock?: StateLock;
  error?: string;
  resources: StateResource[];
}

export interface StackResources {
  units: ResourceUnit[];
  drift?: { run_id: string; at: string; resources: number };
  /** An apply ran but the stack has no state in Firstlume: the code keeps it elsewhere. */
  state_outside?: boolean;
  // Units the latest run found keeping their state outside Firstlume (R2.3).
  outside?: { run_id: string; run_type: RunType; at: string; units: UnitBackend[] };
}

// Where a unit keeps its state: "firstlume", "http" (another server), "local", "s3"...
export interface UnitBackend {
  unit: string;
  backend: string;
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
  unit_backends?: UnitBackend[];
  approved_at?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
}

/** An active run, or one that failed within the last hour. */
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

/** Someone signed in through the OIDC provider (spec 0001, R7.2). */
export interface User {
  id: string;
  login: string;
  name: string;
  email: string;
  avatar_url: string;
  groups: string[];
  last_login_at: string;
}

/** Who is calling: a signed-in user, or the admin token. */
export interface Me {
  kind: "user" | "admin-token";
  user?: User;
  /** Sees every organization and can create them: the admin token, or a user in an admin group. */
  installation_admin: boolean;
  /** The user's organizations; empty for the admin token. */
  organizations: Organization[];
}

/** Someone in an organization. */
export interface Member {
  user_id: string;
  login: string;
  name: string;
  avatar_url: string;
  since: string;
}

/** An invitation waiting for someone's first sign-in. */
export interface Invite {
  id: string;
  login: string;
  invited_by: string;
  created_at: string;
  expires_at: string;
}

export interface MemberList {
  members: Member[];
  invites: Invite[];
}

/** What the login screen offers (R7.14). */
export interface AuthConfig {
  oidc?: { name: string; available: boolean };
  admin_token_login: boolean;
}

// The admin token lives in sessionStorage: it survives reloads but not closing the tab.
// A signed-in user has an HttpOnly session cookie instead, which the browser sends itself.
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

/** The admin token's header, or nothing for a session (the cookie goes by itself). */
function authHeaders(token = getToken()): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function request<T>(method: string, path: string, body?: unknown, token = getToken()): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: {
      ...authHeaders(token),
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  return answer<T>(res);
}

/** Sends files; the browser sets the multipart Content-Type itself. */
async function upload<T>(path: string, form: FormData): Promise<T> {
  const res = await fetch(`/api/v1${path}`, { method: "POST", headers: authHeaders(), body: form });
  return answer<T>(res);
}

async function answer<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    onUnauthorized();
    throw new ApiError(401, "invalid or missing token");
  }
  await throwIfFailed(res);
  if (res.status === 204) return undefined as T;
  return (res.headers.get("Content-Type") ?? "").includes("json") ? res.json() : ((await res.text()) as T);
}

async function throwIfFailed(res: Response) {
  if (res.ok) return;
  let msg = res.statusText;
  try {
    const body = await res.json();
    msg = body.error ?? msg;
    if (body.problems?.length) msg += `: ${body.problems[0].path}: ${body.problems[0].error}`;
  } catch {
    // non-JSON error body
  }
  throw new ApiError(res.status, msg);
}

/** Fetches a whole file: it resolves only once every byte has arrived. */
async function download(path: string): Promise<Blob> {
  const res = await fetch(`/api/v1${path}`, { headers: authHeaders() });
  if (res.status === 401) {
    onUnauthorized();
    throw new ApiError(401, "invalid or missing token");
  }
  await throwIfFailed(res);
  return res.blob();
}

function filesForm(files: File[], fields: Record<string, string> = {}): FormData {
  const form = new FormData();
  for (const [k, v] of Object.entries(fields)) form.append(k, v);
  for (const f of files) form.append("file", f);
  return form;
}

/** Settings live at three kinds of level; this builds the right path for each. */
export function settingsPath(level: { kind: "organization" } | { kind: "folder" | "stack"; id: string }): string {
  if (level.kind === "organization") return "/settings";
  return `/${level.kind === "folder" ? "folders" : "stacks"}/${level.id}/settings`;
}

export const api = {
  // Used by the login screen to check a token before storing it.
  checkToken: (token: string) => request<Organization[]>("GET", "/orgs", undefined, token),
  me: () => request<Me>("GET", "/me"),

  listOrgs: () => request<Organization[]>("GET", "/orgs"),
  createOrg: (slug: string, name: string) => request<Organization>("POST", "/orgs", { slug, name }),

  /** Calls scoped to one organization (by slug). */
  org(slug: string) {
    const p = `/orgs/${encodeURIComponent(slug)}`;
    return {
      get: () => request<Organization>("GET", p),
      /** Renames the organization; a new slug changes its address and the old one keeps working. */
      update: (name: string, slug: string) => request<Organization>("PATCH", p, { name, slug }),
      tree: () => request<Tree>("GET", `${p}/tree`),

      members: () => request<MemberList>("GET", `${p}/members`),
      /** Gives access now to someone who already signed in; otherwise the invitation waits for their first sign-in. */
      invite: (login: string) => request<{ member?: Member; invite?: Invite }>("POST", `${p}/members`, { login }),
      removeMember: (userID: string) => request<void>("DELETE", `${p}/members/${userID}`),
      cancelInvite: (id: string) => request<void>("DELETE", `${p}/invites/${id}`),

      createFolder: (parentID: string, name: string) => request<Folder>("POST", `${p}/folders`, { parent_id: parentID, name }),
      renameFolder: (id: string, name: string) => request<Folder>("PATCH", `${p}/folders/${id}`, { name }),
      deleteFolder: (id: string) => request<void>("DELETE", `${p}/folders/${id}`),

      getSettings: (levelPath: string) => request<SettingsResponse>("GET", `${p}${levelPath}`),
      putSettings: (levelPath: string, s: LevelSettings) => request<SettingsResponse>("PUT", `${p}${levelPath}`, s),

      createStack: (req: CreateStackRequest) => request<Stack>("POST", `${p}/stacks`, req),
      /** Where these states would go in a stack not created yet. Writes nothing. */
      identifyForNewStack: (files: File[], gitRepository: string, ref: string, path: string) =>
        upload<StateIdentifyResult>(`${p}/state/identify`, filesForm(files, { git_repository: gitRepository, ref, path })),
      identifyForStack: (id: string, files: File[]) => upload<StateIdentifyResult>(`${p}/stacks/${id}/state/identify`, filesForm(files)),
      importState: (id: string, files: File[], placement: StatePlacement[], replace: string[], dryRun: boolean) => {
        const form = filesForm(files, { placement: JSON.stringify(placement) });
        for (const unit of replace) form.append("replace", unit);
        return upload<StateImportResult>(`${p}/stacks/${id}/state/import${dryRun ? "?dry_run=true" : ""}`, form);
      },
      getStack: (id: string) => request<Stack>("GET", `${p}/stacks/${id}`),
      updateStack: (id: string, req: UpdateStackRequest) => request<Stack>("PATCH", `${p}/stacks/${id}`, req),
      /** `confirmName` deletes the state with the stack, even if it still manages resources. */
      deleteStack: (id: string, confirmName?: string) =>
        request<void>("DELETE", `${p}/stacks/${id}`, confirmName === undefined ? undefined : { delete_state: true, confirm: confirmName }),
      getStateStorage: () => request<StateStorageSettings>("GET", `${p}/state-storage`),
      /** folderID "" is the organization's top level. */
      previewMove: (id: string, folderID: string) =>
        request<MovePreview>("GET", `${p}/stacks/${id}/move?folder_id=${encodeURIComponent(folderID)}`),
      moveStack: (id: string, folderID: string) => request<Stack>("POST", `${p}/stacks/${id}/move`, { folder_id: folderID }),
      listRuns: (stackID: string, limit = 50) => request<Run[]>("GET", `${p}/stacks/${stackID}/runs?limit=${limit}`),
      createRun: (stackID: string, type: RunType, ref = "") => request<Run>("POST", `${p}/stacks/${stackID}/runs`, { type, ref }),
      stateUnits: (stackID: string) => request<StateUnit[]>("GET", `${p}/stacks/${stackID}/state`),
      resources: (stackID: string) => request<StackResources>("GET", `${p}/stacks/${stackID}/resources`),
      /** The whole stack as a bundle, or one unit ("" is the root) as a plain state. */
      exportState: (stackID: string, unit?: string) =>
        download(`${p}/stacks/${stackID}/state/export${unit === undefined ? "" : `?unit=${encodeURIComponent(unit)}`}`),

      activeRuns: () => request<ActiveRun[]>("GET", `${p}/runs?active=true&failed_within=1h`),
      getRun: (id: string) => request<Run>("GET", `${p}/runs/${id}`),
      changes: (id: string) => request<ResourceChange[]>("GET", `${p}/runs/${id}/changes`),
      steps: (id: string) => request<RunStep[]>("GET", `${p}/runs/${id}/steps`),
      approveRun: (id: string, acknowledged: string[]) => request<Run>("POST", `${p}/runs/${id}/approve`, { acknowledged }),
      discardRun: (id: string) => request<Run>("POST", `${p}/runs/${id}/discard`),
      cancelRun: (id: string) => request<Run>("POST", `${p}/runs/${id}/cancel`),
      logsURL: (id: string) => `/api/v1${p}/runs/${id}/logs`,

      gitConnection: () => request<GitConnection>("GET", `${p}/git/connection`),
      putGitConnection: (req: { provider: string; url: string; account: string; token: string }) =>
        request<GitConnection>("PUT", `${p}/git/connection`, req),
      deleteGitConnection: () => request<void>("DELETE", `${p}/git/connection`),
      vaultConnection: () => request<VaultConnection>("GET", `${p}/vault`),
      putVaultConnection: (req: { address: string; ca_cert?: string; namespace?: string; auth_path?: string; audience?: string }) =>
        request<VaultConnection>("PUT", `${p}/vault`, req),
      deleteVaultConnection: () => request<void>("DELETE", `${p}/vault`),
      vaultPublicKey: () => request<string>("GET", `${p}/vault/public-key`),
      vaultCheck: (stackID: string, phase: "plan" | "apply", paths: string[]) =>
        request<VaultCheckResult>("POST", `${p}/stacks/${stackID}/vault/check`, { phase, paths }),
      declaration: (stackID: string) => request<Declaration>("GET", `${p}/stacks/${stackID}/declaration`),
      gitRepos: () => request<GitRepo[]>("GET", `${p}/git/repos`),
      gitBranches: (fullName: string) => request<string[]>("GET", `${p}/git/repos/${fullName}/branches`),
      discover: (fullName: string, ref: string) =>
        request<DiscoverResponse>("GET", `${p}/git/repos/${fullName}/discover?ref=${encodeURIComponent(ref)}`),

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
      ...authHeaders(),
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

/** What the login screen offers. Public: works before signing in. */
export async function authConfig(): Promise<AuthConfig> {
  const res = await fetch("/auth/config");
  if (!res.ok) throw new ApiError(res.status, res.statusText);
  return res.json();
}

/** Ends the session on the server (R7.3) and forgets the admin token. */
export async function signOut(): Promise<void> {
  setToken(null);
  await fetch("/auth/logout", { method: "POST" }).catch(() => undefined);
}

/** Where "Continue with <provider>" goes; the user comes back to returnTo. */
export function loginURL(returnTo: string): string {
  return `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
}
