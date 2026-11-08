// Package api holds the wire types shared by the server, the agent and the executor.
package api

import "time"

type RunType string

const (
	RunPlan    RunType = "plan"
	RunApply   RunType = "apply"
	RunDestroy RunType = "destroy"
	// RunDrift is a plan that only reports whether the real infrastructure still
	// matches the code. It never applies and never waits for approval.
	RunDrift RunType = "drift"
	// RunCommand runs Terraform, OpenTofu or Terragrunt subcommands from a
	// closed list in the stack's folder (spec 0009). With read access it runs
	// in the plan phase; with write access, in the apply phase, after approval
	// when the stack requires it.
	RunCommand RunType = "command"
)

func (t RunType) Valid() bool {
	return t == RunPlan || t == RunApply || t == RunDestroy || t == RunDrift || t == RunCommand
}

// Applies reports whether runs of this type go on to an apply phase.
func (t RunType) Applies() bool {
	return t == RunApply || t == RunDestroy
}

type Phase string

const (
	PhasePlan  Phase = "plan"
	PhaseApply Phase = "apply"
)

type RunStatus string

const (
	StatusQueued           RunStatus = "queued"
	StatusPlanning         RunStatus = "planning"
	StatusAwaitingApproval RunStatus = "awaiting_approval"
	StatusApplyQueued      RunStatus = "apply_queued"
	StatusApplying         RunStatus = "applying"

	StatusPlanned   RunStatus = "planned"
	StatusNoChanges RunStatus = "no_changes"
	StatusDrifted   RunStatus = "drifted"
	StatusApplied   RunStatus = "applied"
	StatusFailed    RunStatus = "failed"
	StatusDiscarded RunStatus = "discarded"
	StatusCanceled  RunStatus = "canceled"
	// StatusSucceeded ends a command run whose every line exited 0.
	StatusSucceeded RunStatus = "succeeded"
)

// IsTerminal reports whether no further transitions can happen.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case StatusPlanned, StatusNoChanges, StatusDrifted, StatusApplied, StatusFailed, StatusDiscarded, StatusCanceled, StatusSucceeded:
		return true
	}
	return false
}

// RunningPhase returns the phase an executor is running for an in-progress status.
func (s RunStatus) RunningPhase() (Phase, bool) {
	switch s {
	case StatusPlanning:
		return PhasePlan, true
	case StatusApplying:
		return PhaseApply, true
	}
	return "", false
}

// StatusAfterPlan decides where a run goes once its plan phase succeeded.
func StatusAfterPlan(t RunType, hasChanges, autoApprove bool) RunStatus {
	switch {
	case !hasChanges:
		return StatusNoChanges
	case t == RunDrift:
		return StatusDrifted
	case t == RunPlan:
		return StatusPlanned
	case autoApprove:
		return StatusApplyQueued
	default:
		return StatusAwaitingApproval
	}
}

type Organization struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateOrganizationRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type UpdateOrganizationRequest struct {
	Name string `json:"name"`
	// Slug changes the address when set; the old one keeps resolving.
	Slug string `json:"slug,omitempty"`
}

// MaxFolderDepth is how deep folders can nest; a top-level folder has depth 1.
const MaxFolderDepth = 10

type Folder struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	// Path lists the ids of the folder's ancestors from the top level down, ending with its own.
	Path      []string  `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateFolderRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

type UpdateFolderRequest struct {
	Name string `json:"name"`
}

// Stack is one managed infrastructure: the directory holding a .firstlume
// folder in a repository and branch, with its own state, lock and runs.
type Stack struct {
	ID       string `json:"id"`
	FolderID string `json:"folder_id,omitempty"`
	Name     string `json:"name"`
	// Repository is a clone URL.
	Repository  string `json:"repository"`
	Ref         string `json:"ref"`
	Path        string `json:"path"`
	HasGitToken bool   `json:"has_git_token"`
	// ConnectionID is set when the stack clones through the organization's Git connection.
	ConnectionID string `json:"connection_id,omitempty"`
	// Config is the last .firstlume/config.yaml a run read, for display only.
	Config    *StackConfig `json:"config,omitempty"`
	Drift     *Drift       `json:"drift,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type CreateStackRequest struct {
	FolderID string `json:"folder_id"`
	Name     string `json:"name"`
	// GitRepository ("owner/name") picks a repository of the organization's
	// Git connection; the stack then clones with the connection's service
	// token. Otherwise Repository is a clone URL.
	GitRepository string `json:"git_repository,omitempty"`
	// ConnectionID is set by the server for stacks created from the connection.
	ConnectionID string `json:"-"`
	Repository   string `json:"repository"`
	Ref          string `json:"ref"`
	Path         string `json:"path"`
	GitToken     string `json:"git_token"`
}

// MoveStackRequest moves a stack to another folder; an empty FolderID is the
// organization's top level.
type MoveStackRequest struct {
	FolderID string `json:"folder_id"`
}

// MovePreview says what moving a stack to a folder would change in what it
// inherits, and whether the move is possible now.
type MovePreview struct {
	FolderID string `json:"folder_id,omitempty"`
	// ActiveRun is set when a run is queued, planning, awaiting approval or
	// applying: the stack can't move until it ends.
	ActiveRun *Run `json:"active_run,omitempty"`
	// NameTaken is true when the folder already has a stack with this name.
	NameTaken bool         `json:"name_taken,omitempty"`
	Changes   []MoveChange `json:"changes"`
	// OwnSettings and OwnVariables are what the stack sets itself: they go
	// with it and still win. Settings use their JSON names.
	OwnSettings  []string   `json:"own_settings"`
	OwnVariables []Variable `json:"own_variables"`
}

// MoveChange is one inherited setting, variable or registry credential whose
// value would change. Before or After is nil when it is not defined there.
// Sensitive values are never included.
type MoveChange struct {
	// Setting is require_approval, drift_interval_minutes, confirm_destructive,
	// vault_role, variable or registry.
	Setting   string           `json:"setting"`
	Category  VariableCategory `json:"category,omitempty"`
	Key       string           `json:"key,omitempty"`
	Sensitive bool             `json:"sensitive,omitempty"`
	Before    *MoveValue       `json:"before,omitempty"`
	After     *MoveValue       `json:"after,omitempty"`
}

type MoveValue struct {
	Value  string `json:"value,omitempty"`
	Origin Level  `json:"origin"`
}

// UpdateStackRequest changes only the fields that are set. An empty GitToken removes it.
type UpdateStackRequest struct {
	Name       *string `json:"name,omitempty"`
	Repository *string `json:"repository,omitempty"`
	Ref        *string `json:"ref,omitempty"`
	Path       *string `json:"path,omitempty"`
	GitToken   *string `json:"git_token,omitempty"`
}

// StackConfig is .firstlume/config.yaml.
type StackConfig struct {
	Version           int    `json:"version"`
	Engine            string `json:"engine"`
	EngineVersion     string `json:"engine_version"`
	TerragruntVersion string `json:"terragrunt_version,omitempty"`
	// RunAll runs `terragrunt run --all` over every unit below the stack's directory.
	RunAll bool `json:"run_all,omitempty"`
	// Declaration summarizes what the run needs besides the code (spec 0003),
	// without values.
	Declaration *Declaration `json:"declaration,omitempty"`
	// Triggers says which files of the repository count for this stack (spec 0006, R4.2).
	Triggers *StackTriggers `json:"triggers,omitempty"`
}

// StackTriggers are globs relative to the repository's root. Paths also affect
// the stack when they change; Ignore doesn't count, not even inside its path.
type StackTriggers struct {
	Paths  []string `json:"paths,omitempty"`
	Ignore []string `json:"ignore,omitempty"`
}

// Declaration is what .firstlume/config.yaml declares a run needs: files,
// environment and Terraform variables, and registry credentials. References
// say where values come from ("var:NAME", "vault:path#field"); literal values
// are left out.
type Declaration struct {
	Files      []DeclaredFile     `json:"files,omitempty"`
	Env        []DeclaredVariable `json:"env,omitempty"`
	Terraform  []DeclaredVariable `json:"terraform,omitempty"`
	Registries []DeclaredRegistry `json:"registries,omitempty"`
}

type DeclaredFile struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Block  string `json:"block,omitempty"`
	// Sensitive is true when declared so or when a value comes from Vault; a
	// value from a sensitive variable also makes the file sensitive in the run.
	Sensitive bool     `json:"sensitive"`
	Refs      []string `json:"refs,omitempty"`
}

type DeclaredVariable struct {
	Name string `json:"name"`
	// Ref is empty for a literal value.
	Ref string `json:"ref,omitempty"`
}

type DeclaredRegistry struct {
	Host string   `json:"host"`
	Kind string   `json:"kind"`
	Refs []string `json:"refs,omitempty"`
}

// GitConnection links an organization to a Git account. The service token is never returned.
type GitConnection struct {
	Provider    string `json:"provider"`
	URL         string `json:"url"`
	Account     string `json:"account"`
	AccountKind string `json:"account_kind"`
	// WebhookSecretSet tells whether webhooks can be accepted (spec 0006, R1.1).
	WebhookSecretSet bool      `json:"webhook_secret_set"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// WebhookSecret is what a Gitea webhook needs. The secret is only shown here,
// when it is generated.
type WebhookSecret struct {
	URL         string   `json:"url"`
	Secret      string   `json:"secret"`
	ContentType string   `json:"content_type"`
	Events      []string `json:"events"`
}

// What a push on a stack's branch does (spec 0006, R3.1).
const (
	GitOnPushOff   = "off"
	GitOnPushPlan  = "plan"
	GitOnPushApply = "apply"
)

// How a run was started (spec 0006, section 5).
const (
	TriggerManual = "manual"
	TriggerDrift  = "drift"
	TriggerPush   = "push"
)

// RunTrigger says why a run exists.
type RunTrigger struct {
	Kind           string `json:"kind"`
	Actor          string `json:"actor,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	BeforeSHA      string `json:"before_sha,omitempty"`
	PullRequest    int    `json:"pull_request,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
}

// What was decided about a stack for a Git event (spec 0006, R10.1).
const (
	OutcomeRunCreated  = "run_created"
	OutcomeNotAffected = "not_affected"
	OutcomeTriggerOff  = "trigger_off"
	OutcomeOtherBranch = "other_branch"
	OutcomeDuplicate   = "duplicate"
	OutcomeError       = "error"
)

// Why a stack counted as affected.
const (
	AffectedFiles             = "files_changed"
	AffectedForcePush         = "force_push"
	AffectedNewBranch         = "new_branch"
	AffectedCompareFailed     = "compare_failed"
	AffectedCompareIncomplete = "compare_incomplete"
	AffectedConfigUnreadable  = "config_unreadable"
)

// GitEvent is an accepted webhook delivery and what it led to.
type GitEvent struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Repository   string `json:"repository"`
	Ref          string `json:"ref"`
	BeforeSHA    string `json:"before_sha"`
	AfterSHA     string `json:"after_sha"`
	TotalCommits int    `json:"total_commits"`
	Sender       string `json:"sender"`
	Status       string `json:"status"`
	// AffectsAll is why every stack of the branch counted as affected.
	AffectsAll  string          `json:"affects_all,omitempty"`
	Error       string          `json:"error,omitempty"`
	ReceivedAt  time.Time       `json:"received_at"`
	ProcessedAt *time.Time      `json:"processed_at,omitempty"`
	Stacks      []GitEventStack `json:"stacks"`
}

// GitEventStack is the decision about one stack.
type GitEventStack struct {
	StackID   string `json:"stack_id,omitempty"`
	StackName string `json:"stack_name"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	// Files are the files considered, up to a limit; FilesTotal counts them all.
	Files      []string `json:"files,omitempty"`
	FilesTotal int      `json:"files_total,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
}

// PutGitConnectionRequest saves the connection after checking it works. An
// empty Token keeps the stored one.
type PutGitConnectionRequest struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Account  string `json:"account"`
	Token    string `json:"token"`
}

type GitConnectionResponse struct {
	GitConnection
	// Repositories is how many repositories the token sees in the account.
	Repositories int `json:"repositories"`
}

type GitRepo struct {
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Empty         bool   `json:"empty"`
	WebURL        string `json:"web_url"`
}

// StackRef points at a stack, e.g. the one already managing a discovered folder.
type StackRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	FolderID string `json:"folder_id,omitempty"`
}

// DiscoveredStack is a .firstlume folder found in a repository.
type DiscoveredStack struct {
	// Path is the stack directory (the one holding .firstlume); "." for the root.
	Path   string       `json:"path"`
	Config *StackConfig `json:"config,omitempty"`
	// Error says why the folder cannot be used (missing or invalid config.yaml).
	Error string `json:"error,omitempty"`
	// UsedBy is the stack already managing this repository, branch and path.
	UsedBy *StackRef `json:"used_by,omitempty"`
}

type DiscoverResponse struct {
	Repository string            `json:"repository"`
	Ref        string            `json:"ref"`
	Commit     string            `json:"commit"`
	Stacks     []DiscoveredStack `json:"stacks"`
}

// StackSummary is a stack as listed in the navigation tree.
type StackSummary struct {
	Stack
	LastRun *Run `json:"last_run,omitempty"`
}

// Tree is everything needed to draw an organization's navigation.
type Tree struct {
	Folders []Folder       `json:"folders"`
	Stacks  []StackSummary `json:"stacks"`
}

type DriftStatus string

const (
	DriftInSync  DriftStatus = "in_sync"
	DriftDrifted DriftStatus = "drifted"
	DriftError   DriftStatus = "error"
)

// Drift is the outcome of a stack's latest finished drift run.
type Drift struct {
	Status    DriftStatus `json:"status"`
	RunID     string      `json:"run_id,omitempty"`
	CheckedAt time.Time   `json:"checked_at"`
}

// DriftActor is recorded as the creator of scheduled drift runs.
const DriftActor = "drift-detection"

// Drift detection intervals accepted by the API (15 minutes to 7 days).
const (
	MinDriftIntervalMinutes = 15
	MaxDriftIntervalMinutes = 7 * 24 * 60
)

// LevelKind is where a setting can be defined. A stack uses, for each setting,
// the nearest level that defines it: the stack, its folders from the closest
// up, then the organization.
type LevelKind string

const (
	LevelOrganization LevelKind = "organization"
	LevelFolder       LevelKind = "folder"
	LevelStack        LevelKind = "stack"
	// LevelDefault marks a built-in value used when no level defines a setting.
	LevelDefault LevelKind = "default"
	// LevelAgent and LevelInstallation only appear in a run's placement: after
	// the organization come the agent's value and the Helm values (spec 0007).
	LevelAgent        LevelKind = "agent"
	LevelInstallation LevelKind = "installation"
)

// Level identifies one organization, folder or stack.
type Level struct {
	Kind LevelKind `json:"kind"`
	ID   string    `json:"id,omitempty"`
	Name string    `json:"name,omitempty"`
}

// LevelSettings is what a single level defines itself.
type LevelSettings struct {
	// RequireApproval stops applies until someone approves the plan. Nil inherits.
	RequireApproval *bool `json:"require_approval"`
	// DriftIntervalMinutes schedules drift checks; 0 turns them off. Nil inherits.
	DriftIntervalMinutes *int `json:"drift_interval_minutes"`
	// ConfirmDestructive makes approvers tick every resource a plan deletes or replaces. Nil inherits.
	ConfirmDestructive *bool `json:"confirm_destructive"`
	// VaultRole is the role runs log in to Vault with (spec 0003). Nil inherits.
	VaultRole *string `json:"vault_role,omitempty"`
	// GitOnPush is what a push on the stack's branch does (spec 0006, R3.1). Nil inherits.
	GitOnPush *string `json:"git_on_push,omitempty"`
	// Placement is where runs execute (spec 0007). Each field inherits on its own.
	Placement Placement  `json:"placement,omitzero"`
	Variables []Variable `json:"variables"`
	// Registries are this level's registry credentials (spec 0003, R4). Unlike
	// the variables, nil leaves them as they are; an empty list removes them.
	Registries []RegistryCredential `json:"registries,omitempty"`
}

// RegistryCredential logs runs in to a registry: an OCI registry (Harbor,
// GHCR, Docker Hub) or a private Terraform registry. The password is never
// returned; sent empty, the stored one is kept.
type RegistryCredential struct {
	Host string `json:"host"`
	// Kind is oci (the default) or terraform.
	Kind     string `json:"kind,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// PasswordVault reads the password from Vault instead.
	PasswordVault *VaultRef `json:"password_vault,omitempty"`
	// HasPassword says a password is stored.
	HasPassword bool `json:"has_password,omitempty"`
}

type VaultRef struct {
	Path  string `json:"path"`
	Field string `json:"field"`
}

type EffectiveRegistry struct {
	RegistryCredential
	Origin Level `json:"origin"`
	// Overrides is the next level up with a credential for the same host, if any.
	Overrides *Level `json:"overrides,omitempty"`
}

type EffectiveBool struct {
	Value  bool  `json:"value"`
	Origin Level `json:"origin"`
}

type EffectiveInt struct {
	Value  int   `json:"value"`
	Origin Level `json:"origin"`
}

type EffectiveString struct {
	Value  string `json:"value"`
	Origin Level  `json:"origin"`
}

type EffectiveVariable struct {
	Variable
	Origin Level `json:"origin"`
	// Overrides is the next level up that also defines this variable, if any.
	Overrides *Level `json:"overrides,omitempty"`
}

// EffectiveSettings is what applies at a level once inheritance is resolved.
type EffectiveSettings struct {
	RequireApproval      EffectiveBool       `json:"require_approval"`
	DriftIntervalMinutes EffectiveInt        `json:"drift_interval_minutes"`
	ConfirmDestructive   EffectiveBool       `json:"confirm_destructive"`
	VaultRole            EffectiveString     `json:"vault_role"`
	GitOnPush            EffectiveString     `json:"git_on_push"`
	Placement            EffectivePlacement  `json:"placement"`
	Variables            []EffectiveVariable `json:"variables"`
	Registries           []EffectiveRegistry `json:"registries"`
}

type SettingsResponse struct {
	Level     LevelSettings     `json:"level"`
	Effective EffectiveSettings `json:"effective"`
}

type VariableCategory string

const (
	CategoryEnv       VariableCategory = "env"
	CategoryTerraform VariableCategory = "terraform"
	// CategoryFile variables are written as files: the key is the path,
	// relative to the stack's directory, and the value the content (spec 0003).
	CategoryFile VariableCategory = "file"
)

// Variable is returned by the API; sensitive values are never echoed back.
type Variable struct {
	Key       string           `json:"key"`
	Category  VariableCategory `json:"category"`
	Sensitive bool             `json:"sensitive"`
	Value     string           `json:"value,omitempty"`
}

type PutVariablesRequest struct {
	Variables []Variable `json:"variables"`
}

// PlanSummary counts resources by what will happen to them. Unlike Terraform's
// own summary, a replacement counts once, as Replace, not as an add plus a destroy.
type PlanSummary struct {
	Add     int `json:"add"`
	Change  int `json:"change"`
	Replace int `json:"replace"`
	Destroy int `json:"destroy"`
}

type ChangeAction string

const (
	ActionCreate ChangeAction = "create"
	ActionUpdate ChangeAction = "update"
	// ActionReplace deletes the resource and creates it again.
	ActionReplace ChangeAction = "replace"
	ActionDelete  ChangeAction = "delete"
)

// Destructive reports whether the action deletes the existing resource.
func (a ChangeAction) Destructive() bool {
	return a == ActionReplace || a == ActionDelete
}

// ResourceChange is one resource a plan changes, built from `show -json`.
// Sensitive values never appear in it.
type ResourceChange struct {
	// Unit is the Terragrunt unit (relative path) the resource belongs to, in run --all stacks.
	Unit    string       `json:"unit,omitempty"`
	Address string       `json:"address"`
	Type    string       `json:"type"`
	Name    string       `json:"name"`
	Action  ChangeAction `json:"action"`
	// Label says which resource it is (see StateResource.Label): what it
	// will be, or for a delete what it was.
	Label *ResourceLabel `json:"label,omitempty"`
	// Attributes lists what changes (for a create: what it is created with).
	Attributes []AttributeChange `json:"attributes,omitempty"`
	// Truncated says more attributes changed than are listed.
	Truncated bool `json:"truncated,omitempty"`
	// Apply is where the resource is in the apply (spec 0004); nil when the
	// run's apply did not report progress.
	Apply *ResourceApply `json:"apply,omitempty"`
}

type ApplyState string

const (
	ApplyPending  ApplyState = "pending"
	ApplyApplying ApplyState = "applying"
	ApplyDone     ApplyState = "done"
	ApplyFailed   ApplyState = "failed"
	// ApplyNotApplied: the apply finished without starting the resource.
	ApplyNotApplied ApplyState = "not_applied"
)

// ResourceApply is what the apply did to one resource of the plan.
type ResourceApply struct {
	State      ApplyState `json:"state"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	// Steps are the operations in the order they ran: one, or two for a replace.
	Steps  []ApplyStep  `json:"steps,omitempty"`
	Errors []ApplyError `json:"errors,omitempty"`
}

// ApplyStep is one operation Terraform ran on a resource.
type ApplyStep struct {
	// Action is create, update or delete.
	Action     ChangeAction `json:"action"`
	StartedAt  *time.Time   `json:"started_at,omitempty"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Failed     bool         `json:"failed,omitempty"`
}

// ApplyError is an error Terraform reported for a resource, already masked.
type ApplyError struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
	// File and Line point into the configuration, relative to the module's directory.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// ApplyProgress is what the executor reports about one resource: the facts
// seen so far. The server derives the state from them and the plan's action.
type ApplyProgress struct {
	// Key is the ResourceChange key (unit:address, or the address).
	Key    string       `json:"key"`
	Steps  []ApplyStep  `json:"steps,omitempty"`
	Errors []ApplyError `json:"errors,omitempty"`
}

// BackendFirstlume is the backend of a unit whose state is in this
// Firstlume: backend "http" with no address or one under the stack's state URL.
const BackendFirstlume = "firstlume"

// UnitBackend is the state backend a unit's init configured (spec 0002
// R2.3): BackendFirstlume, "http" for another server, "local", "s3" and so on.
type UnitBackend struct {
	// Unit is the unit's directory as in ResourceChange.Unit: empty when the
	// stack is a single unit.
	Unit    string `json:"unit"`
	Backend string `json:"backend"`
}

// ApplyProgressRequest carries the resources that changed since the last
// report. An empty one says the apply reports progress at all.
type ApplyProgressRequest struct {
	Resources []ApplyProgress `json:"resources"`
}

// ApplyTotals counts the outcome of a finished apply that reported progress.
type ApplyTotals struct {
	Done       int `json:"done"`
	Failed     int `json:"failed"`
	NotApplied int `json:"not_applied"`
}

// Key identifies a resource across a run --all plan.
func (c ResourceChange) Key() string {
	if c.Unit == "" {
		return c.Address
	}
	return c.Unit + ":" + c.Address
}

type AttributeChange struct {
	Path string `json:"path"`
	// Before and After are rendered values; nil when the attribute is absent.
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
	// ForcesReplacement marks the attributes that make Terraform recreate the resource.
	ForcesReplacement bool `json:"forces_replacement,omitempty"`
}

// RunSettings records the effective settings and variables a run planned with.
type RunSettings struct {
	RequireApproval      EffectiveBool       `json:"require_approval"`
	ConfirmDestructive   EffectiveBool       `json:"confirm_destructive"`
	DriftIntervalMinutes EffectiveInt        `json:"drift_interval_minutes"`
	VaultRole            EffectiveString     `json:"vault_role"`
	Variables            []EffectiveVariable `json:"variables"`
}

type StepStatus string

const (
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// StepKind tells Firstlume's own steps from hooks declared in .firstlume.
type StepKind string

const (
	StepCore StepKind = "core"
	StepHook StepKind = "hook"
)

// RunStep is one step an executor ran (checkout, init, plan, apply, a hook).
type RunStep struct {
	Phase      Phase      `json:"phase"`
	Name       string     `json:"name"`
	Kind       StepKind   `json:"kind"`
	Status     StepStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// StepRequest starts a step (Status running) or finishes the running one.
type StepRequest struct {
	Name   string     `json:"name"`
	Kind   StepKind   `json:"kind"`
	Status StepStatus `json:"status"`
}

// ApproveRequest confirms a plan. When the stack requires confirming each
// delete or replace, Acknowledged must hold the Key of every such resource.
type ApproveRequest struct {
	Acknowledged []string `json:"acknowledged"`
}

type Run struct {
	ID        string    `json:"id"`
	StackID   string    `json:"stack_id"`
	Type      RunType   `json:"type"`
	Status    RunStatus `json:"status"`
	Ref       string    `json:"ref"`
	CommitSHA string    `json:"commit_sha,omitempty"`
	// Who wrote the commit and its subject line.
	CommitAuthorName  string       `json:"commit_author_name,omitempty"`
	CommitAuthorEmail string       `json:"commit_author_email,omitempty"`
	CommitMessage     string       `json:"commit_message,omitempty"`
	HasChanges        *bool        `json:"has_changes,omitempty"`
	PlanSummary       *PlanSummary `json:"plan_summary,omitempty"`
	Error             string       `json:"error,omitempty"`
	AgentID           string       `json:"agent_id,omitempty"`
	CreatedBy         string       `json:"created_by"`
	ApprovedBy        string       `json:"approved_by,omitempty"`
	// Trigger says why the run exists (spec 0006, section 5).
	Trigger *RunTrigger `json:"trigger,omitempty"`
	// Acknowledged lists the deleted or replaced resources the approver confirmed.
	Acknowledged []string `json:"acknowledged,omitempty"`
	// Config is .firstlume/config.yaml as read at the run's commit.
	Config *StackConfig `json:"config,omitempty"`
	// SettingsUsed is what the plan ran with: effective settings and variables (no secret values).
	SettingsUsed *RunSettings `json:"settings_used,omitempty"`
	// UnitBackends is where each unit keeps its state, as the plan found
	// after init (spec 0002 R2.3). Nil for runs from before it was recorded.
	UnitBackends []UnitBackend `json:"unit_backends,omitempty"`
	// Placement is what the organization, folders and stack defined when the
	// run was created (spec 0007, R2.1). The agent's fields aren't in it.
	Placement *EffectivePlacement `json:"placement,omitempty"`
	// PlacementUsed is all the last phase used, the agent's and the installation's
	// values included. Nil until an agent takes the run.
	PlacementUsed *EffectivePlacement `json:"placement_used,omitempty"`
	// ApplyTotals is set on a single run read, once an apply that reported
	// progress resource by resource has finished.
	ApplyTotals *ApplyTotals `json:"apply_totals,omitempty"`
	// StopRequestedAt is when someone asked to stop the run, and
	// StopRequestedBy who. While the run still executes it is stopping; on a
	// canceled run it says who canceled or stopped it.
	StopRequestedAt *time.Time `json:"stop_requested_at,omitempty"`
	StopRequestedBy string     `json:"stop_requested_by,omitempty"`
	// Command and StateAccess are set on command runs (spec 0009): the
	// command as typed, and "read" or "write".
	Command     string `json:"command,omitempty"`
	StateAccess string `json:"state_access,omitempty"`
	// ExitCode and FailedLine say how a command run that failed ended: the
	// code of the line that failed, and that line (from 1).
	ExitCode   *int `json:"exit_code,omitempty"`
	FailedLine *int `json:"failed_line,omitempty"`
	// StateWrites lists the state versions a command run wrote. Set on a
	// single run read of a finished command run that may write the state.
	StateWrites []StateWrite `json:"state_writes,omitempty"`
	ApprovedAt  *time.Time   `json:"approved_at,omitempty"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// ActiveRun is a run that holds or waits for its stack's lock (or, when asked
// for, one that failed recently), with where its stack lives.
type ActiveRun struct {
	Run
	StackName string `json:"stack_name"`
	// FolderPath names the stack's folders from the top level down.
	FolderPath []string `json:"folder_path"`
}

type CreateRunRequest struct {
	Type RunType `json:"type"`
	Ref  string  `json:"ref"`
	// Command and StateAccess are for command runs only (spec 0009).
	// StateAccess is "read" (the default) or "write".
	Command     string `json:"command,omitempty"`
	StateAccess string `json:"state_access,omitempty"`
}

// StateWrite is a state version a run wrote: the unit (empty for the root)
// and the version's number.
type StateWrite struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
}

// CheckCommandRequest validates a command without creating a run.
type CheckCommandRequest struct {
	Command     string `json:"command"`
	StateAccess string `json:"state_access,omitempty"`
}

// CheckCommandResponse is what a command would run, or why it can't.
type CheckCommandResponse struct {
	OK bool `json:"ok"`
	// Error is set when it can't run: the line (from 1; 0 for the whole
	// command) and the reason.
	Error *CommandError `json:"error,omitempty"`
	// Lines is how many commands it holds, and Writes whether any changes the state.
	Lines  int  `json:"lines"`
	Writes bool `json:"writes"`
}

type CommandError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type Agent struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// Placement is what was set on this agent in the UI or the API (spec 0007, R5).
	Placement Placement `json:"placement"`
	// AcceptsPlacement and InstallPlacement are what the agent last reported.
	// Nil until it asks for work.
	AcceptsPlacement *bool      `json:"accepts_placement,omitempty"`
	InstallPlacement *Placement `json:"install_placement,omitempty"`
}

type CreateAgentRequest struct {
	Name      string    `json:"name"`
	Placement Placement `json:"placement,omitzero"`
}

type CreateAgentResponse struct {
	Agent Agent  `json:"agent"`
	Token string `json:"token"`
}

// --- agent API ---

// Assignment is handed to an agent when it claims a run phase.
type Assignment struct {
	RunID   string  `json:"run_id"`
	StackID string  `json:"stack_id"`
	Type    RunType `json:"type,omitempty"`
	Phase   Phase   `json:"phase"`
	Token   string  `json:"token"`
	// Placement is the run's fields over the agent's. The installation's values
	// are applied by the agent, under these.
	Placement Placement `json:"placement,omitzero"`
}

type ReportFailureRequest struct {
	Phase  Phase  `json:"phase"`
	Reason string `json:"reason"`
}

// --- executor API ---

// ExecSpec is everything an executor needs to run one phase. The engine and
// its version come from .firstlume/config.yaml in the checked-out commit.
type ExecSpec struct {
	RunID      string  `json:"run_id"`
	StackID    string  `json:"stack_id"`
	Type       RunType `json:"type"`
	Phase      Phase   `json:"phase"`
	Repository string  `json:"repository"`
	Ref        string  `json:"ref"`
	CommitSHA  string  `json:"commit_sha,omitempty"`
	Path       string  `json:"path"`
	GitToken   string  `json:"git_token,omitempty"`
	// StateToken authenticates this phase's Terraform to the stack's state
	// backend (spec 0002); the plan phase may only read and lock.
	StateToken string `json:"state_token,omitempty"`
	// Variables are the stack's effective variables. The apply phase gets
	// exactly the ones its plan ran with.
	Variables []ExecVariable `json:"variables"`
	// Vault is set when the organization has a Vault connection (spec 0003).
	Vault *ExecVault `json:"vault,omitempty"`
	// Registries are the stack's effective registry credentials, the
	// password decrypted unless it comes from Vault. The apply gets the plan's.
	Registries []ExecRegistry `json:"registries,omitempty"`
	// Command and StateAccess are set on command runs (spec 0009). The
	// executor checks the command again before running it.
	Command     string `json:"command,omitempty"`
	StateAccess string `json:"state_access,omitempty"`
}

type ExecRegistry struct {
	Host          string    `json:"host"`
	Kind          string    `json:"kind"`
	Username      string    `json:"username,omitempty"`
	Password      string    `json:"password,omitempty"`
	PasswordVault *VaultRef `json:"password_vault,omitempty"`
}

// VaultHashesArtifact holds the keyed hashes of the Vault values a plan
// read; its apply compares them (spec 0003, R3.8).
const VaultHashesArtifact = "vault-hashes"

// ExecVault is how an executor logs in to Vault for one phase.
type ExecVault struct {
	Address   string `json:"address"`
	CACert    string `json:"ca_cert,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	AuthPath  string `json:"auth_path"`
	// Role is the stack's effective Vault role; empty when none is set.
	Role string `json:"role,omitempty"`
	// JWT identifies the stack and the phase to Vault.
	JWT string `json:"jwt"`
	// HashKey keys the hashes of the values a plan read, which its apply
	// compares (R3.8). The same for both phases of a run.
	HashKey string `json:"hash_key"`
}

// VaultConnection is an organization's Vault (spec 0003, R3.1).
type VaultConnection struct {
	Address   string    `json:"address"`
	CACert    string    `json:"ca_cert,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	AuthPath  string    `json:"auth_path"`
	Audience  string    `json:"audience"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PutVaultConnectionRequest struct {
	Address   string `json:"address"`
	CACert    string `json:"ca_cert,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// AuthPath defaults to jwt and Audience to vault.
	AuthPath string `json:"auth_path,omitempty"`
	Audience string `json:"audience,omitempty"`
}

// State storage kinds (spec 0005).
const (
	StateStorageInstallation = "installation"
	StateStorageS3           = "s3"
)

// StateStorage is where an organization's state versions are kept: the
// installation's storage, or an S3 bucket of its own (spec 0005).
type StateStorage struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// For s3. The secret access key is never returned.
	Endpoint           string `json:"endpoint,omitempty"`
	Bucket             string `json:"bucket,omitempty"`
	Region             string `json:"region,omitempty"`
	Folder             string `json:"folder,omitempty"`
	PathStyle          bool   `json:"path_style,omitempty"`
	CACert             string `json:"ca_cert,omitempty"`
	AccessKeyID        string `json:"access_key_id,omitempty"`
	HasSecretAccessKey bool   `json:"has_secret_access_key,omitempty"`
	// Encrypt says how new versions are written; the installation's storage always encrypts.
	Encrypt bool `json:"encrypt"`
	// Versions kept here, and how many of them are in the clear.
	Versions          int `json:"versions"`
	PlaintextVersions int `json:"plaintext_versions,omitempty"`
}

// StateStorageSettings is the organization's current storage and the earlier
// ones that still hold versions (R4.3).
type StateStorageSettings struct {
	Current  StateStorage   `json:"current"`
	Previous []StateStorage `json:"previous"`
}

type PutStateStorageRequest struct {
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	// Region defaults to us-east-1, Folder to firstlume and PathStyle to true.
	Region      string `json:"region,omitempty"`
	Folder      string `json:"folder,omitempty"`
	PathStyle   *bool  `json:"path_style,omitempty"`
	CACert      string `json:"ca_cert,omitempty"`
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey sent empty keeps the stored one.
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	// Encrypt defaults to true; false needs PlaintextAcknowledged (R5.2).
	Encrypt               *bool `json:"encrypt,omitempty"`
	PlaintextAcknowledged bool  `json:"plaintext_acknowledged,omitempty"`
}

// VaultCheckRequest asks whether a stack's role can read paths, without reading them.
type VaultCheckRequest struct {
	Phase Phase    `json:"phase"`
	Paths []string `json:"paths"`
}

type VaultCheckResult struct {
	Role  string           `json:"role"`
	Paths []VaultPathCheck `json:"paths,omitempty"`
	// Error is set when the login itself failed.
	Error string `json:"error,omitempty"`
}

type VaultPathCheck struct {
	Path    string `json:"path"`
	CanRead bool   `json:"can_read"`
	Error   string `json:"error,omitempty"`
}

type ExecVariable struct {
	Key       string           `json:"key"`
	Category  VariableCategory `json:"category"`
	Sensitive bool             `json:"sensitive"`
	Value     string           `json:"value"`
}

type CommitRequest struct {
	SHA         string `json:"sha"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	// Message is the commit's subject line.
	Message string `json:"message,omitempty"`
	// Config is the stack's .firstlume/config.yaml at that commit, when it parsed.
	Config *StackConfig `json:"config,omitempty"`
}

type CompleteRequest struct {
	Success     bool         `json:"success"`
	Error       string       `json:"error,omitempty"`
	HasChanges  bool         `json:"has_changes"`
	PlanSummary *PlanSummary `json:"plan_summary,omitempty"`
	// ExitCode and FailedLine are set when a line of a command run exited
	// with a code other than 0.
	ExitCode   *int `json:"exit_code,omitempty"`
	FailedLine *int `json:"failed_line,omitempty"`
}

// HeartbeatResponse tells the executor whether someone asked to stop the run.
// An empty body (204) means keep going.
type HeartbeatResponse struct {
	StopRequested   bool   `json:"stop_requested"`
	StopRequestedBy string `json:"stop_requested_by,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// StateUnit is one Terraform state of a stack: the root, or a Terragrunt unit
// keyed by its path (spec 0002).
type StateUnit struct {
	Key       string     `json:"key"`
	Version   int        `json:"version"`
	Serial    int64      `json:"serial"`
	Resources int        `json:"resources"`
	Size      int        `json:"size"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	WrittenBy string     `json:"written_by,omitempty"`
	RunID     string     `json:"run_id,omitempty"`
	Lock      *StateLock `json:"lock,omitempty"`
}

type StateLock struct {
	ID        string    `json:"id"`
	Holder    string    `json:"holder"`
	Operation string    `json:"operation,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Since     time.Time `json:"since"`
}

// StackResources is what a stack's state manages, unit by unit (spec 0002
// R8.2). Attribute values never appear in it.
type StackResources struct {
	Units []ResourceUnit `json:"units"`
	// Drift is the latest drift check, when it found differences that no
	// apply has fixed since.
	Drift *ResourceDrift `json:"drift,omitempty"`
	// StateOutside says an apply ran but Firstlume has no state for the
	// stack: its code keeps the state in another backend. Set only for stacks
	// whose runs never recorded their backends (Outside says it then).
	StateOutside bool `json:"state_outside,omitempty"`
	// Outside lists the units whose state is in another backend, from the
	// latest run that recorded the backends (spec 0002 R2.3).
	Outside *OutsideUnits `json:"outside,omitempty"`
}

// OutsideUnits are the units the latest run found keeping their state
// outside Firstlume, and that run.
type OutsideUnits struct {
	RunID   string        `json:"run_id"`
	RunType RunType       `json:"run_type"`
	At      time.Time     `json:"at"`
	Units   []UnitBackend `json:"units"`
}

// ResourceUnit is one unit's current state and the resources in it.
type ResourceUnit struct {
	Key       string     `json:"key"`
	Version   int        `json:"version"`
	Serial    int64      `json:"serial"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	WrittenBy string     `json:"written_by,omitempty"`
	RunID     string     `json:"run_id,omitempty"`
	Lock      *StateLock `json:"lock,omitempty"`
	// Error says why the state could not be read; Resources is then empty.
	Error     string          `json:"error,omitempty"`
	Resources []StateResource `json:"resources"`
}

// StateResource is one resource instance in a state.
type StateResource struct {
	Address string `json:"address"`
	// Mode is managed, or data for a data source.
	Mode   string `json:"mode"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Module string `json:"module,omitempty"`
	// Provider is the provider's source, such as registry.terraform.io/hashicorp/aws.
	Provider string `json:"provider"`
	// Instances counts the instances of its block (count or for_each).
	Instances int      `json:"instances"`
	DependsOn []string `json:"depends_on,omitempty"`
	// Label says which resource it is in plain words, for the types that
	// have a fixed list of identifying attributes.
	Label *ResourceLabel `json:"label,omitempty"`
	// History lists the latest applies that changed it, newest first.
	History []ResourceEvent `json:"history,omitempty"`
	// Drift is what the latest drift check would do to it to match the code.
	Drift ChangeAction `json:"drift,omitempty"`
}

// ResourceLabel is what identifies a resource, read from a fixed list of
// attributes per type (such as a DNS record's name, type and target). No
// other attribute value, and no attribute marked sensitive, is ever read.
type ResourceLabel struct {
	Name string `json:"name"`
	// Record is set for a DNS record.
	Record *DNSRecord `json:"record,omitempty"`
	// Details lists other identifying fields, such as a Helm release's chart.
	Details []LabelDetail `json:"details,omitempty"`
}

// DNSRecord is a DNS record's identifying fields.
type DNSRecord struct {
	Type    string `json:"type,omitempty"`
	Target  string `json:"target,omitempty"`
	Proxied *bool  `json:"proxied,omitempty"`
	// TTL is in seconds; 1 means automatic.
	TTL  int    `json:"ttl,omitempty"`
	Zone string `json:"zone,omitempty"`
}

// LabelDetail is one more identifying field of a resource.
type LabelDetail struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ResourceEvent is an apply that changed a resource.
type ResourceEvent struct {
	Action  ChangeAction `json:"action"`
	RunID   string       `json:"run_id"`
	RunType RunType      `json:"run_type"`
	At      time.Time    `json:"at"`
	By      string       `json:"by"`
}

type ResourceDrift struct {
	RunID     string    `json:"run_id"`
	At        time.Time `json:"at"`
	Resources int       `json:"resources"`
}

// DeleteStackRequest asks to delete the stack's state with it, confirmed by the
// stack's name. Without it, a stack whose state manages resources stays.
type DeleteStackRequest struct {
	DeleteState bool   `json:"delete_state,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
}

// DeleteStateRequest confirms deleting a stack's state by its name.
type DeleteStateRequest struct {
	Confirm string `json:"confirm"`
}

// StateToken lets a pipeline outside Firstlume use a stack's state ("state
// only" mode, spec 0002 R7). The secret is shown only when it is created.
type StateToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Write      bool       `json:"write"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type CreateStateTokenRequest struct {
	Name string `json:"name"`
	// Write allows locking and writing; without it the token only reads.
	Write     bool       `json:"write"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type CreateStateTokenResponse struct {
	StateToken StateToken `json:"state_token"`
	Token      string     `json:"token"`
}

// StateImportUnit is what importing does to one unit (spec 0002 R9.3).
type StateImportUnit struct {
	Key       string `json:"key"`
	Serial    int64  `json:"serial"`
	Lineage   string `json:"lineage"`
	Resources int    `json:"resources"`
	// Status compares the imported state with the unit's current one: new
	// (no state yet), same, newer, older or conflict (another lineage).
	Status         string `json:"status"`
	CurrentVersion int    `json:"current_version,omitempty"`
	CurrentSerial  int64  `json:"current_serial,omitempty"`
	// Action is create, replace or skip. Only units marked to be replaced
	// change a state that already exists (R9.4).
	Action   string `json:"action"`
	LockedBy string `json:"locked_by,omitempty"`
	// Version is the version the import created.
	Version int `json:"version,omitempty"`
}

// StatePlacement says where one uploaded state goes (spec 0002 R9.11).
type StatePlacement struct {
	// Source is the name IdentifiedState gave it.
	Source string `json:"source"`
	Unit   string `json:"unit"`
	// Skip leaves it out of the import.
	Skip bool `json:"skip,omitempty"`
}

// StateLocation is a unit that already holds a state.
type StateLocation struct {
	StackID string `json:"stack_id"`
	Stack   string `json:"stack"`
	Unit    string `json:"unit"`
}

// IdentifiedState says where one uploaded state goes, or why that is left to a
// person. It never carries attribute values (R9.12).
type IdentifiedState struct {
	Source    string `json:"source"`
	Resources int    `json:"resources"`
	Serial    int64  `json:"serial"`
	Lineage   string `json:"lineage"`
	// Placed says Unit holds a suggestion, since "" is the root unit.
	Placed bool   `json:"placed"`
	Unit   string `json:"unit"`
	// Reason is lineage, path, resources, values or only_unit.
	Reason string `json:"reason,omitempty"`
	// Matched of Total of its readable addresses are in the unit's code.
	Matched int `json:"matched"`
	Total   int `json:"total"`
	// Why is tie, no_match, contradicts, older_copy or competing.
	Why        string   `json:"why,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	// PathUnit is the unit by its path alone, to import it anyway (R9.14).
	PathUnit string `json:"path_unit"`
	// AlreadyIn are other stacks with this lineage (R9.13).
	AlreadyIn []StateLocation `json:"already_in,omitempty"`
}

// StateIdentifyResult is where the uploaded states go (R9.9).
type StateIdentifyResult struct {
	// Units are the units the code has; empty if it couldn't be read.
	Units []string `json:"units"`
	// CodeNote says why the code couldn't be read, when so.
	CodeNote string               `json:"code_note,omitempty"`
	States   []IdentifiedState    `json:"states"`
	Ignored  []string             `json:"ignored,omitempty"`
	Problems []StateImportProblem `json:"problems,omitempty"`
}

type StateImportProblem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type StateImportResult struct {
	DryRun bool              `json:"dry_run"`
	Units  []StateImportUnit `json:"units"`
	// Ignored lists files the bundle may carry that are not states, like backups.
	Ignored  []string             `json:"ignored,omitempty"`
	Problems []StateImportProblem `json:"problems,omitempty"`
	Error    string               `json:"error,omitempty"`
}
