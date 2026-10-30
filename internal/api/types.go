// Package api holds the wire types shared by the server, the agent and the executor.
package api

import "time"

type RunType string

const (
	RunPlan    RunType = "plan"
	RunApply   RunType = "apply"
	RunDestroy RunType = "destroy"
)

func (t RunType) Valid() bool {
	return t == RunPlan || t == RunApply || t == RunDestroy
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
	StatusApplied   RunStatus = "applied"
	StatusFailed    RunStatus = "failed"
	StatusDiscarded RunStatus = "discarded"
	StatusCanceled  RunStatus = "canceled"
)

// IsTerminal reports whether no further transitions can happen.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case StatusPlanned, StatusNoChanges, StatusApplied, StatusFailed, StatusDiscarded, StatusCanceled:
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
	case t == RunPlan:
		return StatusPlanned
	case autoApprove:
		return StatusApplyQueued
	default:
		return StatusAwaitingApproval
	}
}

type Template struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Repository        string    `json:"repository"`
	Path              string    `json:"path"`
	DefaultRef        string    `json:"default_ref"`
	Engine            string    `json:"engine"`
	EngineVersion     string    `json:"engine_version"`
	TerragruntVersion string    `json:"terragrunt_version,omitempty"`
	HasGitToken       bool      `json:"has_git_token"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateTemplateRequest struct {
	Name              string `json:"name"`
	Repository        string `json:"repository"`
	Path              string `json:"path"`
	DefaultRef        string `json:"default_ref"`
	Engine            string `json:"engine"`
	EngineVersion     string `json:"engine_version"`
	TerragruntVersion string `json:"terragrunt_version"`
	GitToken          string `json:"git_token"`
}

type Environment struct {
	ID          string    `json:"id"`
	TemplateID  string    `json:"template_id"`
	Name        string    `json:"name"`
	Ref         string    `json:"ref,omitempty"`
	AutoApprove bool      `json:"auto_approve"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateEnvironmentRequest struct {
	TemplateID  string `json:"template_id"`
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	AutoApprove bool   `json:"auto_approve"`
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

type PlanSummary struct {
	Add     int `json:"add"`
	Change  int `json:"change"`
	Destroy int `json:"destroy"`
}

type Run struct {
	ID            string       `json:"id"`
	EnvironmentID string       `json:"environment_id"`
	Type          RunType      `json:"type"`
	Status        RunStatus    `json:"status"`
	Ref           string       `json:"ref"`
	CommitSHA     string       `json:"commit_sha,omitempty"`
	HasChanges    *bool        `json:"has_changes,omitempty"`
	PlanSummary   *PlanSummary `json:"plan_summary,omitempty"`
	Error         string       `json:"error,omitempty"`
	AgentID       string       `json:"agent_id,omitempty"`
	CreatedBy     string       `json:"created_by"`
	ApprovedBy    string       `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time   `json:"approved_at,omitempty"`
	StartedAt     *time.Time   `json:"started_at,omitempty"`
	FinishedAt    *time.Time   `json:"finished_at,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
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
	RunID         string `json:"run_id"`
	EnvironmentID string `json:"environment_id"`
	Phase         Phase  `json:"phase"`
	Token         string `json:"token"`
}

type ReportFailureRequest struct {
	Phase  Phase  `json:"phase"`
	Reason string `json:"reason"`
}

// --- executor API ---

// ExecSpec is everything an executor needs to run one phase.
type ExecSpec struct {
	RunID             string         `json:"run_id"`
	EnvironmentID     string         `json:"environment_id"`
	Type              RunType        `json:"type"`
	Phase             Phase          `json:"phase"`
	Repository        string         `json:"repository"`
	Ref               string         `json:"ref"`
	CommitSHA         string         `json:"commit_sha,omitempty"`
	Path              string         `json:"path"`
	Engine            string         `json:"engine"`
	EngineVersion     string         `json:"engine_version"`
	TerragruntVersion string         `json:"terragrunt_version,omitempty"`
	GitToken          string         `json:"git_token,omitempty"`
	Variables         []ExecVariable `json:"variables"`
}

type ExecVariable struct {
	Key       string           `json:"key"`
	Category  VariableCategory `json:"category"`
	Sensitive bool             `json:"sensitive"`
	Value     string           `json:"value"`
}

type CommitRequest struct {
	SHA string `json:"sha"`
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
