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
)

func (t RunType) Valid() bool {
	return t == RunPlan || t == RunApply || t == RunDestroy || t == RunDrift
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
)

// IsTerminal reports whether no further transitions can happen.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case StatusPlanned, StatusNoChanges, StatusDrifted, StatusApplied, StatusFailed, StatusDiscarded, StatusCanceled:
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
	Provider    string    `json:"provider"`
	URL         string    `json:"url"`
	Account     string    `json:"account"`
	AccountKind string    `json:"account_kind"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
	VaultRole *string    `json:"vault_role,omitempty"`
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
	// Acknowledged lists the deleted or replaced resources the approver confirmed.
	Acknowledged []string `json:"acknowledged,omitempty"`
	// Config is .firstlume/config.yaml as read at the run's commit.
	Config *StackConfig `json:"config,omitempty"`
	// SettingsUsed is what the plan ran with: effective settings and variables (no secret values).
	SettingsUsed *RunSettings `json:"settings_used,omitempty"`
	// ApplyTotals is set on a single run read, once an apply that reported
	// progress resource by resource has finished.
	ApplyTotals *ApplyTotals `json:"apply_totals,omitempty"`
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
}

type Agent struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type CreateAgentRequest struct {
	Name string `json:"name"`
}

type CreateAgentResponse struct {
	Agent Agent  `json:"agent"`
	Token string `json:"token"`
}

// --- agent API ---

// Assignment is handed to an agent when it claims a run phase.
type Assignment struct {
	RunID   string `json:"run_id"`
	StackID string `json:"stack_id"`
	Phase   Phase  `json:"phase"`
	Token   string `json:"token"`
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
