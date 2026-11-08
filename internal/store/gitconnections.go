package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// GitConnection is an organization's connection with its decrypted service token.
type GitConnection struct {
	api.GitConnection
	ID    string
	Token string
}

func (s *Store) GetGitConnection(ctx context.Context, orgID string) (*GitConnection, error) {
	var c GitConnection
	var enc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider, url, account, account_kind, token_enc, webhook_secret_enc IS NOT NULL, webhook_secret_at, created_at, updated_at
		FROM git_connections WHERE organization_id = $1`, orgID).Scan(
		&c.ID, &c.Provider, &c.URL, &c.Account, &c.AccountKind, &enc, &c.WebhookSecretSet, &c.WebhookSecretAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	tok, err := s.keys.Decrypt(enc)
	if err != nil {
		return nil, err
	}
	c.Token = string(tok)
	return &c, nil
}

// PutGitConnection creates or replaces the organization's connection. It
// refuses to change the provider while stacks clone through the connection,
// with ErrConflict, and a new provider drops the webhook secret, whose format
// is the provider's (spec 0010, R2.6, R5.1).
func (s *Store) PutGitConnection(ctx context.Context, orgID string, c api.GitConnection, token string) (*api.GitConnection, error) {
	enc, err := s.keys.Encrypt([]byte(token))
	if err != nil {
		return nil, err
	}
	var out api.GitConnection
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var provider string
		var inUse bool
		err := tx.QueryRow(ctx, `
			SELECT c.provider, EXISTS (SELECT 1 FROM stacks WHERE connection_id = c.id)
			FROM git_connections c WHERE c.organization_id = $1 FOR UPDATE`, orgID).Scan(&provider, &inUse)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case provider != c.Provider && inUse:
			return ErrConflict
		}
		return tx.QueryRow(ctx, `
			INSERT INTO git_connections (organization_id, provider, url, account, account_kind, token_enc)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (organization_id) DO UPDATE SET provider = EXCLUDED.provider, url = EXCLUDED.url,
			       account = EXCLUDED.account, account_kind = EXCLUDED.account_kind, token_enc = EXCLUDED.token_enc,
			       webhook_secret_enc = CASE WHEN git_connections.provider = EXCLUDED.provider THEN git_connections.webhook_secret_enc END,
			       webhook_secret_at = CASE WHEN git_connections.provider = EXCLUDED.provider THEN git_connections.webhook_secret_at END,
			       updated_at = now()
			RETURNING provider, url, account, account_kind, webhook_secret_enc IS NOT NULL, webhook_secret_at, created_at, updated_at`,
			orgID, c.Provider, c.URL, c.Account, c.AccountKind, enc).Scan(
			&out.Provider, &out.URL, &out.Account, &out.AccountKind, &out.WebhookSecretSet, &out.WebhookSecretAt, &out.CreatedAt, &out.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteGitConnection refuses while stacks clone through the connection.
func (s *Store) DeleteGitConnection(ctx context.Context, orgID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var inUse bool
		err := tx.QueryRow(ctx, `
			SELECT c.id, EXISTS (SELECT 1 FROM stacks WHERE connection_id = c.id)
			FROM git_connections c WHERE c.organization_id = $1 FOR UPDATE`, orgID).Scan(&id, &inUse)
		if err != nil {
			return notFound(err)
		}
		if inUse {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `DELETE FROM git_connections WHERE id = $1`, id)
		return err
	})
}

// StacksUsing maps stack directory → stack for an organization's stacks on a repository and ref.
func (s *Store) StacksUsing(ctx context.Context, orgID, repository, ref string) (map[string]api.StackRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT path, id, name, folder_id FROM stacks WHERE organization_id = $1 AND repository = $2 AND ref = $3`,
		orgID, repository, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]api.StackRef{}
	for rows.Next() {
		var p string
		var r api.StackRef
		var folder *string
		if err := rows.Scan(&p, &r.ID, &r.Name, &folder); err != nil {
			return nil, err
		}
		r.FolderID = deref(folder)
		out[p] = r
	}
	return out, rows.Err()
}
