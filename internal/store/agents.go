package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
)

// AgentIdentity is the authenticated caller of the agent API.
type AgentIdentity struct {
	ID             string
	OrganizationID string
	Name           string
	// Placement is what was set on the agent in the UI or the API.
	Placement api.Placement
}

const agentColumns = `id, name, last_seen_at, created_at, placement, accepts_placement, install_placement`

func scanAgent(row pgx.Row) (api.Agent, error) {
	var a api.Agent
	var placement, install []byte
	if err := row.Scan(&a.ID, &a.Name, &a.LastSeenAt, &a.CreatedAt, &placement, &a.AcceptsPlacement, &install); err != nil {
		return a, err
	}
	if err := json.Unmarshal(placement, &a.Placement); err != nil {
		return a, err
	}
	if install != nil {
		a.InstallPlacement = &api.Placement{}
		if err := json.Unmarshal(install, a.InstallPlacement); err != nil {
			return a, err
		}
	}
	return a, nil
}

func (s *Store) CreateAgent(ctx context.Context, orgID, name string, placement api.Placement) (*api.CreateAgentResponse, error) {
	token, err := secrets.RandomToken("fla_")
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(placement)
	if err != nil {
		return nil, err
	}
	a, err := scanAgent(s.pool.QueryRow(ctx, `
		INSERT INTO agents (organization_id, name, token_hash, placement) VALUES ($1, $2, $3, $4)
		RETURNING `+agentColumns, orgID, name, secrets.HashToken(token), raw))
	if err != nil {
		return nil, classify(err)
	}
	return &api.CreateAgentResponse{Agent: a, Token: token}, nil
}

// SetAgentPlacement replaces what is set on an agent. It applies from the next
// phase the agent takes, with no restart (spec 0007, R5.4).
func (s *Store) SetAgentPlacement(ctx context.Context, orgID, id string, placement api.Placement) (*api.Agent, error) {
	raw, err := json.Marshal(placement)
	if err != nil {
		return nil, err
	}
	a, err := scanAgent(s.pool.QueryRow(ctx, `
		UPDATE agents SET placement = $3 WHERE organization_id = $1 AND id = $2
		RETURNING `+agentColumns, orgID, id, raw))
	if err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// reportAgent records what an agent says about itself when it asks for work,
// so the screens can show it (R5.2).
func (s *Store) reportAgent(ctx context.Context, agent *AgentIdentity, req api.ClaimRequest) error {
	install, err := json.Marshal(req.InstallPlacement)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE agents SET accepts_placement = $2, install_placement = $3
		WHERE id = $1 AND (accepts_placement IS DISTINCT FROM $2 OR install_placement IS DISTINCT FROM $3::jsonb)`,
		agent.ID, req.AcceptsPlacement, install)
	return err
}

// EnsureAgent registers (or re-keys) an agent with a token chosen by the operator.
// It is used to bootstrap the agent that ships in the same Helm release.
func (s *Store) EnsureAgent(ctx context.Context, orgID, name, token string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agents (organization_id, name, token_hash) VALUES ($1, $2, $3)
		ON CONFLICT (organization_id, name) DO UPDATE SET token_hash = EXCLUDED.token_hash`,
		orgID, name, secrets.HashToken(token))
	return err
}

func (s *Store) ListAgents(ctx context.Context, orgID string) ([]api.Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+agentColumns+` FROM agents WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAgent(ctx context.Context, orgID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE organization_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthenticateAgent resolves a bearer token and records that the agent is alive.
func (s *Store) AuthenticateAgent(ctx context.Context, token string) (*AgentIdentity, error) {
	var id AgentIdentity
	var placement []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE agents SET last_seen_at = now() WHERE token_hash = $1
		RETURNING id, organization_id, name, placement`, secrets.HashToken(token)).Scan(&id.ID, &id.OrganizationID, &id.Name, &placement)
	if err != nil {
		return nil, notFound(err)
	}
	if err := json.Unmarshal(placement, &id.Placement); err != nil {
		return nil, err
	}
	return &id, nil
}
