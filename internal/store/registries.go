package store

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// Registry credentials (spec 0003, F3): set on any level and inherited by
// host, the nearest level winning, like variables.

type registryRow struct {
	api.RegistryCredential
	enc []byte
}

func levelRegistries(ctx context.Context, q querier, orgID string, levels []api.Level) (map[levelKey][]registryRow, error) {
	var folders []string
	var stackID *string
	for _, l := range levels {
		switch l.Kind {
		case api.LevelFolder:
			folders = append(folders, l.ID)
		case api.LevelStack:
			stackID = &l.ID
		}
	}
	rows, err := q.Query(ctx, `
		SELECT folder_id, stack_id, host, kind, coalesce(username, ''), password_enc, vault_path, vault_field
		FROM registry_credentials
		WHERE organization_id = $1 AND (folder_id = ANY($2::uuid[]) OR stack_id = $3::uuid OR (folder_id IS NULL AND stack_id IS NULL))
		ORDER BY host`, orgID, folders, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[levelKey][]registryRow{}
	for rows.Next() {
		var f, st, path, field *string
		var r registryRow
		if err := rows.Scan(&f, &st, &r.Host, &r.Kind, &r.Username, &r.enc, &path, &field); err != nil {
			return nil, err
		}
		if path != nil {
			r.PasswordVault = &api.VaultRef{Path: *path, Field: deref(field)}
		}
		r.HasPassword = r.enc != nil
		k := levelKey{deref(f), deref(st)}
		out[k] = append(out[k], r)
	}
	return out, rows.Err()
}

type resolvedRegistry struct {
	api.EffectiveRegistry
	enc []byte
}

// resolveRegistries picks, for each host, the nearest level's credential.
func resolveRegistries(levels []api.Level, regs map[levelKey][]registryRow) []resolvedRegistry {
	winners := map[string]*resolvedRegistry{}
	for _, l := range levels { // nearest first
		for _, r := range regs[keyOf(l)] {
			if w, ok := winners[r.Host]; ok {
				if w.Overrides == nil {
					w.Overrides = &l
				}
				continue
			}
			winners[r.Host] = &resolvedRegistry{EffectiveRegistry: api.EffectiveRegistry{RegistryCredential: r.RegistryCredential, Origin: l}, enc: r.enc}
		}
	}
	out := make([]resolvedRegistry, 0, len(winners))
	for _, w := range winners {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// effectiveRegistries fills in the registries that apply at the end of levels.
func effectiveRegistries(ctx context.Context, q querier, orgID string, levels []api.Level) ([]resolvedRegistry, map[levelKey][]registryRow, error) {
	regs, err := levelRegistries(ctx, q, orgID, levels)
	if err != nil {
		return nil, nil, err
	}
	return resolveRegistries(levels, regs), regs, nil
}

func publicRegistries(resolved []resolvedRegistry) []api.EffectiveRegistry {
	out := make([]api.EffectiveRegistry, 0, len(resolved))
	for _, r := range resolved {
		out = append(out, r.EffectiveRegistry)
	}
	return out
}

// putRegistries replaces a level's credentials. A credential sent without a
// password or a Vault reference keeps the stored password of the same host.
func (s *Store) putRegistries(ctx context.Context, tx pgx.Tx, orgID string, folderID, stackID *string, in []api.RegistryCredential) error {
	const level = `organization_id = $1 AND folder_id IS NOT DISTINCT FROM $2::uuid AND stack_id IS NOT DISTINCT FROM $3::uuid`
	existing := map[string][]byte{}
	rows, err := tx.Query(ctx, `SELECT host, password_enc FROM registry_credentials WHERE `+level+` AND password_enc IS NOT NULL FOR UPDATE`,
		orgID, folderID, stackID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var host string
		var enc []byte
		if err := rows.Scan(&host, &enc); err != nil {
			rows.Close()
			return err
		}
		existing[host] = enc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM registry_credentials WHERE `+level, orgID, folderID, stackID); err != nil {
		return err
	}
	for _, r := range in {
		var enc []byte
		var path, field *string
		switch {
		case r.PasswordVault != nil:
			path, field = &r.PasswordVault.Path, &r.PasswordVault.Field
		case r.Password != "":
			if enc, err = s.keys.Encrypt([]byte(r.Password)); err != nil {
				return err
			}
		default:
			if enc = existing[r.Host]; enc == nil {
				return ErrMissingSecret
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO registry_credentials (organization_id, folder_id, stack_id, host, kind, username, password_enc, vault_path, vault_field)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			orgID, folderID, stackID, r.Host, r.Kind, nullable(r.Username), enc, path, field); err != nil {
			return classify(err)
		}
	}
	return nil
}

// execRegistries decrypts a stack's effective registry credentials for an executor.
func (s *Store) execRegistries(resolved []resolvedRegistry) ([]api.ExecRegistry, error) {
	out := make([]api.ExecRegistry, 0, len(resolved))
	for _, r := range resolved {
		e := api.ExecRegistry{Host: r.Host, Kind: r.Kind, Username: r.Username, PasswordVault: r.PasswordVault}
		if r.enc != nil {
			dec, err := s.keys.Decrypt(r.enc)
			if err != nil {
				return nil, err
			}
			e.Password = string(dec)
		}
		out = append(out, e)
	}
	return out, nil
}
