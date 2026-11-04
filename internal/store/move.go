package store

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// ErrRunInProgress means the stack has a run queued, planning, awaiting
// approval or applying, so it can't move: a plan made with one folder's
// settings must not be applied with another's.
var ErrRunInProgress = errors.New("run in progress")

// stackChainIn is the chain of levels a stack would inherit from if it were
// in folderID ("" for the top level). An unknown folder is ErrInvalid.
func stackChainIn(ctx context.Context, q querier, orgID string, stack api.Level, folderID string) ([]api.Level, error) {
	base := api.Level{Kind: api.LevelOrganization}
	if folderID != "" {
		base = api.Level{Kind: api.LevelFolder, ID: folderID}
	}
	rest, err := chain(ctx, q, orgID, base)
	if errors.Is(err, ErrNotFound) && folderID != "" {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return append([]api.Level{stack}, rest...), nil
}

// inherited is everything that applies at the end of a chain, with the
// encrypted values needed to compare secrets.
type inherited struct {
	eff      api.EffectiveSettings
	vars     []resolvedVariable
	regs     []resolvedRegistry
	settings map[levelKey]settingsRow
	own      map[levelKey][]variableRow
}

func inheritedAt(ctx context.Context, q querier, orgID string, levels []api.Level) (*inherited, error) {
	settings, vars, err := levelData(ctx, q, orgID, levels)
	if err != nil {
		return nil, err
	}
	eff, resolved := resolve(levels, settings, vars)
	regs, _, err := effectiveRegistries(ctx, q, orgID, levels)
	if err != nil {
		return nil, err
	}
	return &inherited{eff: eff, vars: resolved, regs: regs, settings: settings, own: vars}, nil
}

// PreviewMoveStack compares what the stack inherits now with what it would
// inherit in folderID, and says whether it can move there now.
func (s *Store) PreviewMoveStack(ctx context.Context, orgID, stackID, folderID string) (*api.MovePreview, error) {
	now, err := chain(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: stackID})
	if err != nil {
		return nil, err
	}
	stack := now[0]
	after, err := stackChainIn(ctx, s.pool, orgID, stack, folderID)
	if err != nil {
		return nil, err
	}
	before, err := inheritedAt(ctx, s.pool, orgID, now)
	if err != nil {
		return nil, err
	}
	moved, err := inheritedAt(ctx, s.pool, orgID, after)
	if err != nil {
		return nil, err
	}
	out := &api.MovePreview{FolderID: folderID, OwnSettings: []string{}, OwnVariables: []api.Variable{}}
	if out.Changes, err = s.moveChanges(before, moved); err != nil {
		return nil, err
	}

	own := before.settings[keyOf(stack)]
	for name, set := range map[string]bool{
		"require_approval":       own.requireApproval != nil,
		"drift_interval_minutes": own.driftInterval != nil,
		"confirm_destructive":    own.confirmDestructive != nil,
		"vault_role":             own.vaultRole != nil,
		"git_on_push":            own.gitOnPush != nil,
	} {
		if set {
			out.OwnSettings = append(out.OwnSettings, name)
		}
	}
	slices.Sort(out.OwnSettings)
	for _, v := range before.own[keyOf(stack)] {
		if v.Sensitive {
			v.Value = ""
		}
		out.OwnVariables = append(out.OwnVariables, v.Variable)
	}

	if out.ActiveRun, err = activeRun(ctx, s.pool, stackID); err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM stacks WHERE organization_id = $1 AND id <> $2
			AND folder_id IS NOT DISTINCT FROM $3::uuid AND lower(name) = lower($4))`,
		orgID, stackID, nullable(folderID), stack.Name).Scan(&out.NameTaken)
	return out, err
}

// MoveStack puts the stack in folderID ("" for the top level). It refuses
// while a run is in progress, and a folder that already has a stack with the
// same name is ErrDuplicate. State versions stay where they were written; new
// ones follow the new folder names.
func (s *Store) MoveStack(ctx context.Context, orgID, stackID, folderID string) (*api.Stack, error) {
	var st *api.Stack
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := scanStack(tx.QueryRow(ctx,
			`SELECT `+stackColumns+` FROM stacks st WHERE st.organization_id = $1 AND st.id = $2 FOR UPDATE`, orgID, stackID)); err != nil {
			return err
		}
		run, err := activeRun(ctx, tx, stackID)
		if err != nil {
			return err
		}
		if run != nil {
			return ErrRunInProgress
		}
		if folderID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM folders WHERE organization_id = $1 AND id = $2)`,
				orgID, folderID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrInvalid
			}
		}
		st, err = scanStack(tx.QueryRow(ctx, `
			WITH st AS (
				UPDATE stacks SET folder_id = $3, updated_at = now()
				WHERE organization_id = $1 AND id = $2 RETURNING *
			)
			SELECT `+stackColumns+` FROM st`, orgID, stackID, nullable(folderID)))
		return classify(err)
	})
	return st, err
}

func activeRun(ctx context.Context, q querier, stackID string) (*api.Run, error) {
	run, err := scanRun(q.QueryRow(ctx, `SELECT `+runColumns+` FROM runs r
		WHERE r.stack_id = $1 AND r.status IN `+activeStatuses+` ORDER BY r.created_at LIMIT 1`, stackID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return run, err
}

// moveChanges lists what differs between two resolutions. A secret counts as
// changed only when its value does.
func (s *Store) moveChanges(before, after *inherited) ([]api.MoveChange, error) {
	out := []api.MoveChange{}
	setting := func(name, a, b string, oa, ob api.Level) {
		if a != b {
			out = append(out, api.MoveChange{Setting: name, Before: &api.MoveValue{Value: a, Origin: oa}, After: &api.MoveValue{Value: b, Origin: ob}})
		}
	}
	be, ae := before.eff, after.eff
	setting("require_approval", strconv.FormatBool(be.RequireApproval.Value), strconv.FormatBool(ae.RequireApproval.Value),
		be.RequireApproval.Origin, ae.RequireApproval.Origin)
	setting("drift_interval_minutes", strconv.Itoa(be.DriftIntervalMinutes.Value), strconv.Itoa(ae.DriftIntervalMinutes.Value),
		be.DriftIntervalMinutes.Origin, ae.DriftIntervalMinutes.Origin)
	setting("confirm_destructive", strconv.FormatBool(be.ConfirmDestructive.Value), strconv.FormatBool(ae.ConfirmDestructive.Value),
		be.ConfirmDestructive.Origin, ae.ConfirmDestructive.Origin)
	setting("git_on_push", be.GitOnPush.Value, ae.GitOnPush.Value, be.GitOnPush.Origin, ae.GitOnPush.Origin)
	if be.VaultRole.Value != ae.VaultRole.Value {
		c := api.MoveChange{Setting: "vault_role"}
		if be.VaultRole.Value != "" {
			c.Before = &api.MoveValue{Value: be.VaultRole.Value, Origin: be.VaultRole.Origin}
		}
		if ae.VaultRole.Value != "" {
			c.After = &api.MoveValue{Value: ae.VaultRole.Value, Origin: ae.VaultRole.Origin}
		}
		out = append(out, c)
	}

	type varKey struct {
		category api.VariableCategory
		key      string
	}
	afterVars := map[varKey]resolvedVariable{}
	for _, v := range after.vars {
		afterVars[varKey{v.Category, v.Key}] = v
	}
	value := func(v resolvedVariable) *api.MoveValue {
		mv := &api.MoveValue{Value: v.Value, Origin: v.Origin}
		if v.Sensitive {
			mv.Value = ""
		}
		return mv
	}
	seen := map[varKey]bool{}
	for _, b := range before.vars {
		k := varKey{b.Category, b.Key}
		seen[k] = true
		a, ok := afterVars[k]
		if ok {
			same, err := s.sameVariable(b, a)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		c := api.MoveChange{Setting: "variable", Category: b.Category, Key: b.Key, Sensitive: b.Sensitive || a.Sensitive, Before: value(b)}
		if ok {
			c.After = value(a)
		}
		out = append(out, c)
	}
	for _, a := range after.vars {
		if !seen[varKey{a.Category, a.Key}] {
			out = append(out, api.MoveChange{Setting: "variable", Category: a.Category, Key: a.Key, Sensitive: a.Sensitive, After: value(a)})
		}
	}

	afterRegs := map[string]resolvedRegistry{}
	for _, r := range after.regs {
		afterRegs[r.Host] = r
	}
	reg := func(r resolvedRegistry) *api.MoveValue { return &api.MoveValue{Value: r.Username, Origin: r.Origin} }
	for _, b := range before.regs {
		a, ok := afterRegs[b.Host]
		delete(afterRegs, b.Host)
		if ok {
			same, err := s.sameRegistry(b, a)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		c := api.MoveChange{Setting: "registry", Key: b.Host, Sensitive: true, Before: reg(b)}
		if ok {
			c.After = reg(a)
		}
		out = append(out, c)
	}
	for _, a := range after.regs { // keeps the hosts' order
		if _, ok := afterRegs[a.Host]; ok {
			out = append(out, api.MoveChange{Setting: "registry", Key: a.Host, Sensitive: true, After: reg(a)})
		}
	}
	return out, nil
}

func (s *Store) sameVariable(a, b resolvedVariable) (bool, error) {
	if a.Origin == b.Origin {
		return true, nil
	}
	if a.Sensitive != b.Sensitive {
		return false, nil
	}
	if !a.Sensitive {
		return a.Value == b.Value, nil
	}
	return s.sameSecret(a.enc, b.enc)
}

func (s *Store) sameRegistry(a, b resolvedRegistry) (bool, error) {
	if a.Origin == b.Origin {
		return true, nil
	}
	if a.Kind != b.Kind || a.Username != b.Username || !reflect.DeepEqual(a.PasswordVault, b.PasswordVault) {
		return false, nil
	}
	return s.sameSecret(a.enc, b.enc)
}

func (s *Store) sameSecret(a, b []byte) (bool, error) {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b), nil
	}
	da, err := s.keys.Decrypt(a)
	if err != nil {
		return false, err
	}
	db, err := s.keys.Decrypt(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(da, db), nil
}
