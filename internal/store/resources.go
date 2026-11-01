package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// historyRuns bounds how many of the stack's latest applies are read to tell
// when each resource last changed, and historyDepth how many are kept per resource.
const (
	historyRuns  = 200
	historyDepth = 5
)

// stateResources is the part of a state the Resources tab reads. Attribute
// values are read only by the labels, from a fixed list per type.
type stateResources struct {
	Resources []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Instances []struct {
			IndexKey     json.RawMessage            `json:"index_key"`
			Dependencies []string                   `json:"dependencies"`
			Attributes   map[string]json.RawMessage `json:"attributes"`
			Sensitive    json.RawMessage            `json:"sensitive_attributes"`
		} `json:"instances"`
	} `json:"resources"`
}

var providerSource = regexp.MustCompile(`^provider\["([^"]+)"\]`)

// listResources returns a state's resource instances in the state's order.
func listResources(data []byte) ([]api.StateResource, error) {
	var st stateResources
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	zones := zoneNames{}
	for _, r := range st.Resources {
		for _, in := range r.Instances {
			if a, ok := newLabelAttrs(in.Attributes, in.Sensitive); ok {
				zones.add(r.Type, a)
			}
		}
	}
	out := []api.StateResource{}
	for _, r := range st.Resources {
		provider := r.Provider
		if m := providerSource.FindStringSubmatch(provider); m != nil {
			provider = m[1]
		}
		base := r.Type + "." + r.Name
		if r.Mode == "data" {
			base = "data." + base
		}
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, in := range r.Instances {
			addr := base
			// A string key is a JSON string, which Terraform's addresses quote the same way.
			if k := string(in.IndexKey); k != "" && k != "null" {
				addr += "[" + k + "]"
			}
			var label *api.ResourceLabel
			if a, ok := newLabelAttrs(in.Attributes, in.Sensitive); ok {
				label = resourceLabel(r.Mode, r.Type, a, zones)
			}
			out = append(out, api.StateResource{
				Address: addr, Mode: r.Mode, Type: r.Type, Name: r.Name, Module: r.Module, Provider: provider,
				Instances: len(r.Instances), DependsOn: in.Dependencies, Label: label,
			})
		}
	}
	return out, nil
}

// StackResources lists what the stack's state manages, unit by unit, with the
// applies that last changed each resource and what the latest drift check
// found (spec 0002 R8.2).
func (s *Store) StackResources(ctx context.Context, orgID, stackID string) (*api.StackResources, error) {
	units, err := s.ListStateUnits(ctx, orgID, stackID)
	if err != nil {
		return nil, err
	}
	history, err := s.resourceHistory(ctx, stackID)
	if err != nil {
		return nil, err
	}
	drift, driftActions, err := s.openDrift(ctx, stackID)
	if err != nil {
		return nil, err
	}
	out := &api.StackResources{Units: []api.ResourceUnit{}, Drift: drift}
	for _, u := range units {
		if u.Version == 0 {
			continue
		}
		ru := api.ResourceUnit{Key: u.Key, Version: u.Version, Serial: u.Serial, UpdatedAt: u.UpdatedAt,
			WrittenBy: u.WrittenBy, RunID: u.RunID, Lock: u.Lock, Resources: []api.StateResource{}}
		res, err := s.unitResources(ctx, stackID, u.Key)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			ru.Error = fmt.Sprintf("the state of version %d could not be read (%v)", u.Version, err)
		}
		for _, r := range res {
			key := api.ResourceChange{Unit: u.Key, Address: r.Address}.Key()
			r.History = history[key]
			r.Drift = driftActions[key]
			ru.Resources = append(ru.Resources, r)
		}
		out.Units = append(out.Units, ru)
	}
	if len(out.Units) == 0 {
		err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM runs WHERE stack_id = $1 AND type IN ('apply', 'destroy') AND status = 'applied')`,
			stackID).Scan(&out.StateOutside)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) unitResources(ctx context.Context, stackID, key string) ([]api.StateResource, error) {
	data, err := s.stateBytes(ctx, stackID, key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, errors.New("no state")
	}
	return listResources(data)
}

// resourceHistory returns, by change key, the latest applies that changed
// each resource. A failed apply counts only for what it reported done.
func (s *Store) resourceHistory(ctx context.Context, stackID string) (map[string][]api.ResourceEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, type, status, apply_tracked, changes, coalesce(finished_at, updated_at), coalesce(approved_by, created_by)
		FROM runs
		WHERE stack_id = $1 AND type IN ('apply', 'destroy') AND changes IS NOT NULL
		  AND (status = 'applied' OR (status = 'failed' AND apply_tracked))
		ORDER BY coalesce(finished_at, updated_at) DESC LIMIT $2`, stackID, historyRuns)
	if err != nil {
		return nil, err
	}
	type applied struct {
		id      string
		typ     api.RunType
		status  api.RunStatus
		tracked bool
		changes []byte
		at      time.Time
		by      string
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (applied, error) {
		var a applied
		err := row.Scan(&a.id, &a.typ, &a.status, &a.tracked, &a.changes, &a.at, &a.by)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]api.ResourceEvent{}
	for _, run := range runs {
		var changes []api.ResourceChange
		if err := json.Unmarshal(run.changes, &changes); err != nil {
			return nil, err
		}
		var progress map[string]*applyRecord
		if run.status == api.StatusFailed {
			if progress, err = s.applyProgress(ctx, run.id); err != nil {
				return nil, err
			}
		}
		for _, c := range changes {
			key := c.Key()
			if progress != nil && deriveApply(c.Action, progress[key], true).State != api.ApplyDone {
				continue
			}
			if len(out[key]) < historyDepth {
				out[key] = append(out[key], api.ResourceEvent{Action: c.Action, RunID: run.id, RunType: run.typ, At: run.at, By: run.by})
			}
		}
	}
	return out, nil
}

// openDrift returns the latest drift check when it found differences and no
// apply has finished since, with what it would do to each resource.
func (s *Store) openDrift(ctx context.Context, stackID string) (*api.ResourceDrift, map[string]api.ChangeAction, error) {
	var d api.ResourceDrift
	var status api.RunStatus
	var changes []byte
	err := s.pool.QueryRow(ctx, `
		SELECT d.id, d.status, d.changes, d.finished_at FROM runs d
		WHERE d.stack_id = $1 AND d.type = 'drift' AND d.status IN ('drifted', 'no_changes') AND d.finished_at IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM runs a WHERE a.stack_id = d.stack_id AND a.type IN ('apply', 'destroy')
		                  AND a.status = 'applied' AND a.finished_at > d.finished_at)
		ORDER BY d.finished_at DESC LIMIT 1`, stackID).Scan(&d.RunID, &status, &changes, &d.At)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != api.StatusDrifted) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var list []api.ResourceChange
	if changes != nil {
		if err := json.Unmarshal(changes, &list); err != nil {
			return nil, nil, err
		}
	}
	actions := make(map[string]api.ChangeAction, len(list))
	for _, c := range list {
		actions[c.Key()] = c.Action
	}
	d.Resources = len(list)
	return &d, actions, nil
}
