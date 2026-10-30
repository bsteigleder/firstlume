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
	// Config is the last .firstlume/config.yaml a run read, for display only.
	Config    *StackConfig `json:"config,omitempty"`
	Drift     *Drift       `json:"drift,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type CreateStackRequest struct {
	FolderID   string `json:"folder_id"`
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Path       string `json:"path"`
	GitToken   string `json:"git_token"`
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
	ConfirmDestructive *bool      `json:"confirm_destructive"`
	Variables          []Variable `json:"variables"`
}

type EffectiveBool struct {
	Value  bool  `json:"value"`
	Origin Level `json:"origin"`
}

type EffectiveInt struct {
	Value  int   `json:"value"`
	Origin Level `json:"origin"`
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
	Variables            []EffectiveVariable `json:"variables"`
}

type SettingsResponse struct {
	Level     LevelSettings     `json:"level"`
	Effective EffectiveSettings `json:"effective"`
}

type VariableCategory string

const (
	CategoryEnv       VariableCategory = "env"
	CategoryTerraform VariableCategory = "terraform"
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
	ApprovedAt   *time.Time   `json:"approved_at,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// ActiveRun is a run that holds or waits for its stack's lock, with where its stack lives.
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
	// Variables are the stack's effective variables. The apply phase gets
	// exactly the ones its plan ran with.
	Variables []ExecVariable `json:"variables"`
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
