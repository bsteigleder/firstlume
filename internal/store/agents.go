package store

import (
	"context"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
)

// AgentIdentity is the authenticated caller of the agent API.
type AgentIdentity struct {
	ID             string
	OrganizationID string
	Name           string
}

func (s *Store) CreateAgent(ctx context.Context, orgID, name string) (*api.CreateAgentResponse, error) {
	token, err := secrets.RandomToken("fla_")
	if err != nil {
		return nil, err
	}
	var a api.Agent
	err = s.pool.QueryRow(ctx, `
		INSERT INTO agents (organization_id, name, token_hash) VALUES ($1, $2, $3)
		RETURNING id, name, last_seen_at, created_at`,
		orgID, name, secrets.HashToken(token)).Scan(&a.ID, &a.Name, &a.LastSeenAt, &a.CreatedAt)
	if err != nil {
		return nil, classify(err)
	}
	return &api.CreateAgentResponse{Agent: a, Token: token}, nil
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
		SELECT id, name, last_seen_at, created_at FROM agents WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Agent{}
	for rows.Next() {
		var a api.Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.LastSeenAt, &a.CreatedAt); err != nil {
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
	err := s.pool.QueryRow(ctx, `
		UPDATE agents SET last_seen_at = now() WHERE token_hash = $1
		RETURNING id, organization_id, name`, secrets.HashToken(token)).Scan(&id.ID, &id.OrganizationID, &id.Name)
	if err != nil {
		return nil, notFound(err)
	}
	return &id, nil
}
