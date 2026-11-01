package store

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/vault"
)

// Vault (spec 0003, F2): the key Firstlume signs run JWTs with, each
// organization's Vault connection, and what a run's JWT says about its stack.

// SigningKey returns the key run JWTs are signed with, creating it the first
// time. Concurrent servers agree on one key: the first insert wins. The key's
// row is named after the master key, which is the only one that can open it.
func (s *Store) SigningKey(ctx context.Context) (*vault.SigningKey, error) {
	s.signingMu.Lock()
	defer s.signingMu.Unlock()
	if s.signingKey != nil {
		return s.signingKey, nil
	}
	runJWTKeyID := "run-jwt-" + hex.EncodeToString(s.keys.Sign([]byte("run-jwt-signing-key"))[:8])
	var enc []byte
	err := s.pool.QueryRow(ctx, `SELECT private_enc FROM signing_keys WHERE id = $1`, runJWTKeyID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		enc, err = s.createSigningKey(ctx, runJWTKeyID)
	}
	if err != nil {
		return nil, err
	}
	der, err := s.keys.Decrypt(enc)
	if err != nil {
		return nil, err
	}
	if s.signingKey, err = vault.ParseSigningKey(der); err != nil {
		return nil, err
	}
	return s.signingKey, nil
}

// createSigningKey stores a new key under id, unless another server just
// did, and returns the stored one.
func (s *Store) createSigningKey(ctx context.Context, id string) ([]byte, error) {
	_, der, err := vault.NewSigningKey()
	if err != nil {
		return nil, err
	}
	sealed, err := s.keys.Encrypt(der)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO signing_keys (id, private_enc) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		id, sealed); err != nil {
		return nil, err
	}
	var enc []byte
	err = s.pool.QueryRow(ctx, `SELECT private_enc FROM signing_keys WHERE id = $1`, id).Scan(&enc)
	return enc, err
}

func (s *Store) GetVaultConnection(ctx context.Context, orgID string) (*api.VaultConnection, error) {
	var c api.VaultConnection
	var ca, ns *string
	err := s.pool.QueryRow(ctx, `
		SELECT address, ca_cert, namespace, auth_path, audience, created_at, updated_at
		FROM vault_connections WHERE organization_id = $1`, orgID).
		Scan(&c.Address, &ca, &ns, &c.AuthPath, &c.Audience, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	c.CACert, c.Namespace = deref(ca), deref(ns)
	return &c, nil
}

func (s *Store) PutVaultConnection(ctx context.Context, orgID string, in api.PutVaultConnectionRequest) (*api.VaultConnection, error) {
	if in.AuthPath == "" {
		in.AuthPath = "jwt"
	}
	if in.Audience == "" {
		in.Audience = "vault"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO vault_connections (organization_id, address, ca_cert, namespace, auth_path, audience)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (organization_id) DO UPDATE SET address = EXCLUDED.address, ca_cert = EXCLUDED.ca_cert,
			namespace = EXCLUDED.namespace, auth_path = EXCLUDED.auth_path, audience = EXCLUDED.audience, updated_at = now()`,
		orgID, in.Address, nullable(in.CACert), nullable(in.Namespace), in.AuthPath, in.Audience)
	if err != nil {
		return nil, classify(err)
	}
	return s.GetVaultConnection(ctx, orgID)
}

func (s *Store) DeleteVaultConnection(ctx context.Context, orgID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM vault_connections WHERE organization_id = $1`, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StackVault is what a stack needs to log in to Vault: the organization's
// connection, the stack's effective role and the claims about the stack.
type StackVault struct {
	Connection *api.VaultConnection
	Role       string
	Org        string
	StackID    string
	StackName  string
	// StackPath is the folders' names and the stack's, joined by "/".
	StackPath string
	FolderIDs []string
}

// StackVault returns nil when the organization has no Vault connection. An
// empty orgID is looked up from the stack.
func (s *Store) StackVault(ctx context.Context, orgID, stackID string) (*StackVault, error) {
	if orgID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT organization_id FROM stacks WHERE id = $1`, stackID).Scan(&orgID); err != nil {
			return nil, notFound(err)
		}
	}
	conn, err := s.GetVaultConnection(ctx, orgID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at := api.Level{Kind: api.LevelStack, ID: stackID}
	eff, _, err := s.effective(ctx, s.pool, orgID, at)
	if err != nil {
		return nil, err
	}
	levels, err := chain(ctx, s.pool, orgID, at)
	if err != nil {
		return nil, err
	}
	sv := &StackVault{Connection: conn, Role: eff.VaultRole.Value, StackID: stackID}
	var names []string
	for i := len(levels) - 1; i >= 0; i-- { // from the organization down
		switch l := levels[i]; l.Kind {
		case api.LevelFolder:
			names = append(names, l.Name)
			sv.FolderIDs = append(sv.FolderIDs, l.ID)
		case api.LevelStack:
			names = append(names, l.Name)
			sv.StackName = l.Name
		}
	}
	sv.StackPath = strings.Join(names, "/")
	if err := s.pool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, orgID).Scan(&sv.Org); err != nil {
		return nil, err
	}
	return sv, nil
}

// RunJWTClaims are the claims of a run phase's JWT (R3.3).
func (sv *StackVault) RunJWTClaims(issuer, runID string, runType api.RunType, phase string, now time.Time) map[string]any {
	return map[string]any{
		"iss":        issuer,
		"aud":        sv.Connection.Audience,
		"sub":        "stack:" + sv.StackID,
		"org":        sv.Org,
		"stack_id":   sv.StackID,
		"stack_name": sv.StackName,
		"stack_path": sv.StackPath,
		"folder_ids": append([]string{}, sv.FolderIDs...),
		"run_id":     runID,
		"run_type":   string(runType),
		"phase":      phase,
		"iat":        now.Unix(),
		"nbf":        now.Add(-30 * time.Second).Unix(),
		"exp":        now.Add(15 * time.Minute).Unix(),
	}
}
