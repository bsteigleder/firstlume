package store

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// Settings are inherited: a stack uses, for each setting and variable, the
// nearest level that defines it: the stack itself, its folders from the
// closest up, then the organization (spec 0001, section 4.6).

// Built-in values for settings no level defines.
const (
	defaultRequireApproval      = true
	defaultDriftIntervalMinutes = 0
	defaultConfirmDestructive   = false
)

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// levelKey identifies a level the way level_settings and variables rows do.
type levelKey struct{ folderID, stackID string }

func keyOf(l api.Level) levelKey {
	switch l.Kind {
	case api.LevelFolder:
		return levelKey{folderID: l.ID}
	case api.LevelStack:
		return levelKey{stackID: l.ID}
	}
	return levelKey{}
}

// chain returns the levels whose settings apply at the given level, nearest
// first. It returns ErrNotFound when the level is not in the organization.
func chain(ctx context.Context, q querier, orgID string, at api.Level) ([]api.Level, error) {
	var org api.Level
	if err := q.QueryRow(ctx, `SELECT id, name FROM organizations WHERE id = $1`, orgID).Scan(&org.ID, &org.Name); err != nil {
		return nil, notFound(err)
	}
	org.Kind = api.LevelOrganization

	var levels []api.Level
	var folderPath []string
	switch at.Kind {
	case api.LevelOrganization:
		return []api.Level{org}, nil
	case api.LevelStack:
		st := api.Level{Kind: api.LevelStack, ID: at.ID}
		err := q.QueryRow(ctx, `
			SELECT st.name, coalesce(f.path::text[], '{}') FROM stacks st LEFT JOIN folders f ON f.id = st.folder_id
			WHERE st.organization_id = $1 AND st.id = $2`, orgID, at.ID).Scan(&st.Name, &folderPath)
		if err != nil {
			return nil, notFound(err)
		}
		levels = append(levels, st)
	case api.LevelFolder:
		err := q.QueryRow(ctx, `SELECT path::text[] FROM folders WHERE organization_id = $1 AND id = $2`,
			orgID, at.ID).Scan(&folderPath)
		if err != nil {
			return nil, notFound(err)
		}
	default:
		return nil, ErrInvalid
	}

	names := map[string]string{}
	if len(folderPath) > 0 {
		rows, err := q.Query(ctx, `SELECT id, name FROM folders WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
			orgID, folderPath)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			names[id] = name
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for i := len(folderPath) - 1; i >= 0; i-- {
		levels = append(levels, api.Level{Kind: api.LevelFolder, ID: folderPath[i], Name: names[folderPath[i]]})
	}
	return append(levels, org), nil
}

type settingsRow struct {
	requireApproval    *bool
	driftInterval      *int
	confirmDestructive *bool
	vaultRole          *string
}

type variableRow struct {
	api.Variable
	enc []byte
}

// levelData loads what each level in the chain defines.
func levelData(ctx context.Context, q querier, orgID string, levels []api.Level) (map[levelKey]settingsRow, map[levelKey][]variableRow, error) {
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
	const where = `organization_id = $1 AND (folder_id = ANY($2::uuid[]) OR stack_id = $3::uuid OR (folder_id IS NULL AND stack_id IS NULL))`

	settings := map[levelKey]settingsRow{}
	rows, err := q.Query(ctx, `SELECT folder_id, stack_id, require_approval, drift_interval_minutes, confirm_destructive, vault_role
		FROM level_settings WHERE `+where,
		orgID, folders, stackID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var f, st *string
		var r settingsRow
		if err := rows.Scan(&f, &st, &r.requireApproval, &r.driftInterval, &r.confirmDestructive, &r.vaultRole); err != nil {
			rows.Close()
			return nil, nil, err
		}
		settings[levelKey{deref(f), deref(st)}] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	vars := map[levelKey][]variableRow{}
	rows, err = q.Query(ctx, `SELECT folder_id, stack_id, category, key, sensitive, value, value_enc FROM variables WHERE `+where+`
		ORDER BY category, key`, orgID, folders, stackID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f, st, plain *string
		var v variableRow
		if err := rows.Scan(&f, &st, &v.Category, &v.Key, &v.Sensitive, &plain, &v.enc); err != nil {
			return nil, nil, err
		}
		v.Value = deref(plain)
		k := levelKey{deref(f), deref(st)}
		vars[k] = append(vars[k], v)
	}
	return settings, vars, rows.Err()
}

type resolvedVariable struct {
	api.EffectiveVariable
	enc []byte
}

// resolve applies the nearest-level-wins rule over a chain (nearest first).
func resolve(levels []api.Level, settings map[levelKey]settingsRow, vars map[levelKey][]variableRow) (api.EffectiveSettings, []resolvedVariable) {
	def := api.Level{Kind: api.LevelDefault}
	eff := api.EffectiveSettings{
		RequireApproval:      api.EffectiveBool{Value: defaultRequireApproval, Origin: def},
		DriftIntervalMinutes: api.EffectiveInt{Value: defaultDriftIntervalMinutes, Origin: def},
		ConfirmDestructive:   api.EffectiveBool{Value: defaultConfirmDestructive, Origin: def},
		VaultRole:            api.EffectiveString{Origin: def},
	}
	for i := len(levels) - 1; i >= 0; i-- { // farthest first, so nearer levels overwrite
		r := settings[keyOf(levels[i])]
		if r.requireApproval != nil {
			eff.RequireApproval = api.EffectiveBool{Value: *r.requireApproval, Origin: levels[i]}
		}
		if r.driftInterval != nil {
			eff.DriftIntervalMinutes = api.EffectiveInt{Value: *r.driftInterval, Origin: levels[i]}
		}
		if r.confirmDestructive != nil {
			eff.ConfirmDestructive = api.EffectiveBool{Value: *r.confirmDestructive, Origin: levels[i]}
		}
		if r.vaultRole != nil {
			eff.VaultRole = api.EffectiveString{Value: *r.vaultRole, Origin: levels[i]}
		}
	}

	type varKey struct{ category, key string }
	winners := map[varKey]*resolvedVariable{}
	var order []varKey
	for _, l := range levels { // nearest first: the first definition wins, the next one is what it overrides
		for _, v := range vars[keyOf(l)] {
			k := varKey{string(v.Category), v.Key}
			if w, ok := winners[k]; ok {
				if w.Overrides == nil {
					w.Overrides = &l
				}
				continue
			}
			winners[k] = &resolvedVariable{EffectiveVariable: api.EffectiveVariable{Variable: v.Variable, Origin: l}, enc: v.enc}
			order = append(order, k)
		}
	}
	slices.SortFunc(order, func(a, b varKey) int {
		if a.category != b.category {
			if a.category < b.category {
				return -1
			}
			return 1
		}
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return 0
	})
	out := make([]resolvedVariable, 0, len(order))
	eff.Variables = make([]api.EffectiveVariable, 0, len(order))
	for _, k := range order {
		w := winners[k]
		out = append(out, *w)
		ev := w.EffectiveVariable
		if ev.Sensitive {
			ev.Value = "" // never returned
		}
		eff.Variables = append(eff.Variables, ev)
	}
	return eff, out
}

func (s *Store) effective(ctx context.Context, q querier, orgID string, at api.Level) (api.EffectiveSettings, []resolvedVariable, error) {
	levels, err := chain(ctx, q, orgID, at)
	if err != nil {
		return api.EffectiveSettings{}, nil, err
	}
	settings, vars, err := levelData(ctx, q, orgID, levels)
	if err != nil {
		return api.EffectiveSettings{}, nil, err
	}
	eff, resolved := resolve(levels, settings, vars)
	regs, _, err := effectiveRegistries(ctx, q, orgID, levels)
	if err != nil {
		return api.EffectiveSettings{}, nil, err
	}
	eff.Registries = publicRegistries(regs)
	return eff, resolved, nil
}

// GetSettings returns what a level defines itself and what applies there once
// inheritance is resolved. Sensitive values are never included.
func (s *Store) GetSettings(ctx context.Context, orgID string, at api.Level) (*api.SettingsResponse, error) {
	levels, err := chain(ctx, s.pool, orgID, at)
	if err != nil {
		return nil, err
	}
	settings, vars, err := levelData(ctx, s.pool, orgID, levels)
	if err != nil {
		return nil, err
	}
	eff, _ := resolve(levels, settings, vars)
	resolvedRegs, regs, err := effectiveRegistries(ctx, s.pool, orgID, levels)
	if err != nil {
		return nil, err
	}
	eff.Registries = publicRegistries(resolvedRegs)
	own := settings[keyOf(at)]
	out := &api.SettingsResponse{
		Level: api.LevelSettings{
			RequireApproval:      own.requireApproval,
			DriftIntervalMinutes: own.driftInterval,
			ConfirmDestructive:   own.confirmDestructive,
			VaultRole:            own.vaultRole,
			Variables:            []api.Variable{},
			Registries:           []api.RegistryCredential{},
		},
		Effective: eff,
	}
	for _, r := range regs[keyOf(at)] {
		out.Level.Registries = append(out.Level.Registries, r.RegistryCredential)
	}
	for _, v := range vars[keyOf(at)] {
		if v.Sensitive {
			v.Value = ""
		}
		out.Level.Variables = append(out.Level.Variables, v.Variable)
	}
	return out, nil
}

// PutSettings replaces what a level defines. A sensitive variable sent with an
// empty value keeps its stored value, since the API never returns it.
func (s *Store) PutSettings(ctx context.Context, orgID string, at api.Level, in api.LevelSettings) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := chain(ctx, tx, orgID, at); err != nil {
			return err
		}
		k := keyOf(at)
		folderID, stackID := nullable(k.folderID), nullable(k.stackID)
		const level = `organization_id = $1 AND folder_id IS NOT DISTINCT FROM $2::uuid AND stack_id IS NOT DISTINCT FROM $3::uuid`

		if _, err := tx.Exec(ctx, `DELETE FROM level_settings WHERE `+level, orgID, folderID, stackID); err != nil {
			return err
		}
		if in.RequireApproval != nil || in.DriftIntervalMinutes != nil || in.ConfirmDestructive != nil || in.VaultRole != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO level_settings (organization_id, folder_id, stack_id, require_approval, drift_interval_minutes, confirm_destructive, vault_role)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				orgID, folderID, stackID, in.RequireApproval, in.DriftIntervalMinutes, in.ConfirmDestructive, in.VaultRole); err != nil {
				return classify(err)
			}
		}

		existing := map[string][]byte{}
		rows, err := tx.Query(ctx, `SELECT category, key, value_enc FROM variables WHERE `+level+` AND sensitive FOR UPDATE`,
			orgID, folderID, stackID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cat, key string
			var enc []byte
			if err := rows.Scan(&cat, &key, &enc); err != nil {
				rows.Close()
				return err
			}
			existing[cat+"/"+key] = enc
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM variables WHERE `+level, orgID, folderID, stackID); err != nil {
			return err
		}
		for _, v := range in.Variables {
			var plain *string
			var enc []byte
			switch {
			case !v.Sensitive:
				plain = &v.Value
			case v.Value != "":
				if enc, err = s.keys.Encrypt([]byte(v.Value)); err != nil {
					return err
				}
			default:
				if enc = existing[string(v.Category)+"/"+v.Key]; enc == nil {
					return ErrMissingSecret
				}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO variables (organization_id, folder_id, stack_id, category, key, sensitive, value, value_enc)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				orgID, folderID, stackID, v.Category, v.Key, v.Sensitive, plain, enc); err != nil {
				return classify(err)
			}
		}
		if in.Registries != nil {
			return s.putRegistries(ctx, tx, orgID, folderID, stackID, in.Registries)
		}
		return nil
	})
}

// execVariables decrypts a stack's effective variables for an executor.
func (s *Store) execVariables(resolved []resolvedVariable) ([]api.ExecVariable, error) {
	out := make([]api.ExecVariable, 0, len(resolved))
	for _, r := range resolved {
		v := api.ExecVariable{Key: r.Key, Category: r.Category, Sensitive: r.Sensitive, Value: r.Value}
		if r.Sensitive {
			dec, err := s.keys.Decrypt(r.enc)
			if err != nil {
				return nil, err
			}
			v.Value = string(dec)
		}
		out = append(out, v)
	}
	return out, nil
}
