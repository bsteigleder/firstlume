package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
)

// StateTokenPrefix starts every stack state token (spec 0002, R7), so the
// state API tells them apart from the run phases' signed tokens.
const StateTokenPrefix = "flst_"

// CreateStateToken creates a token for the stack's state. The token itself is
// returned only here; only its hash is stored (R7.2).
func (s *Store) CreateStateToken(ctx context.Context, orgID, stackID string, req api.CreateStateTokenRequest, actor string) (*api.CreateStateTokenResponse, error) {
	token, err := secrets.RandomToken(StateTokenPrefix)
	if err != nil {
		return nil, err
	}
	out := api.CreateStateTokenResponse{Token: token}
	t := &out.StateToken
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO state_tokens (organization_id, stack_id, name, can_write, token_hash, created_by, expires_at)
			SELECT organization_id, id, $3, $4, $5, $6, $7 FROM stacks WHERE organization_id = $1 AND id = $2
			RETURNING id, name, can_write, created_by, created_at, expires_at`,
			orgID, stackID, req.Name, req.Write, secrets.HashToken(token), actor, req.ExpiresAt).
			Scan(&t.ID, &t.Name, &t.Write, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt)
		if err != nil {
			return classify(err)
		}
		return stateEvent(ctx, tx, orgID, stackID, nil, "token_created", actor, map[string]any{"token": t.Name, "write": t.Write})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListStateTokens returns the stack's tokens that have not been revoked.
func (s *Store) ListStateTokens(ctx context.Context, orgID, stackID string) ([]api.StateToken, error) {
	if err := s.stackExists(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, can_write, created_by, created_at, expires_at, last_used_at FROM state_tokens
		WHERE organization_id = $1 AND stack_id = $2 AND revoked_at IS NULL ORDER BY name`, orgID, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.StateToken{}
	for rows.Next() {
		var t api.StateToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Write, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeStateToken stops a token from working. A lock it holds stays until
// someone unlocks it (R7.4).
func (s *Store) RevokeStateToken(ctx context.Context, orgID, stackID, tokenID, actor string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `
			UPDATE state_tokens SET revoked_at = now()
			WHERE organization_id = $1 AND stack_id = $2 AND id = $3 AND revoked_at IS NULL
			RETURNING name`, orgID, stackID, tokenID).Scan(&name)
		if err != nil {
			return notFound(err)
		}
		return stateEvent(ctx, tx, orgID, stackID, nil, "token_revoked", actor, map[string]any{"token": name})
	})
}

// StateTokenActor returns the actor for a stack state token that is neither
// revoked nor expired, and records its use.
func (s *Store) StateTokenActor(ctx context.Context, token string) (*StateActor, error) {
	var a StateActor
	var name string
	err := s.pool.QueryRow(ctx, `
		UPDATE state_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, organization_id, stack_id, name, can_write`, secrets.HashToken(token)).
		Scan(&a.TokenID, &a.OrganizationID, &a.StackID, &name, &a.CanWrite)
	if err != nil {
		return nil, notFound(err)
	}
	// A read-only token never holds a lock: plans use -lock=false (R7.3, D6).
	a.CanLock = a.CanWrite
	a.Name = "token " + name
	return &a, nil
}

// stateEvent records something that happened to a stack's state (R8.7);
// unitKey is nil when it concerns the whole stack.
func stateEvent(ctx context.Context, tx pgx.Tx, orgID, stackID string, unitKey *string, kind, actor string, detail map[string]any) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO state_events (organization_id, stack_id, unit_key, kind, actor, detail) VALUES ($1, $2, $3, $4, $5, $6)`,
		orgID, stackID, unitKey, kind, actor, detail)
	return err
}
