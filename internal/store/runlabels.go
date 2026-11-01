package store

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/reslabel"
)

// labelOldChanges names the changes of a run planned before plans carried
// labels: a create by the values its plan stored (else, once applied, by the
// state), the others by the stack's current state. What it finds is saved
// with the run, so each run is labelled once.
func (s *Store) labelOldChanges(ctx context.Context, runID, stackID string, changes []api.ResourceChange, stored []byte) error {
	units := map[string]bool{}
	for _, c := range changes {
		if c.Label != nil {
			return nil // planned with labels
		}
		if reslabel.Known(c.Type) {
			units[c.Unit] = true
		}
	}
	if len(units) == 0 {
		return nil
	}
	inState := map[string]*api.ResourceLabel{}
	zones := reslabel.Zones{}
	for unit := range units {
		res, err := s.unitResourcesZones(ctx, stackID, unit, zones)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue // an unreadable state names nothing
		}
		for _, r := range res {
			if r.Label != nil {
				inState[api.ResourceChange{Unit: unit, Address: r.Address}.Key()] = r.Label
			}
		}
	}
	filled := false
	for i := range changes {
		c := &changes[i]
		if !reslabel.Known(c.Type) {
			continue
		}
		if c.Action == api.ActionCreate {
			c.Label = reslabel.Label("managed", c.Type, reslabel.FromChange(c.Attributes), zones)
		}
		if c.Label == nil {
			c.Label = inState[c.Key()]
		}
		filled = filled || c.Label != nil
	}
	if !filled {
		return nil
	}
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	// Only if the run's changes are still the ones read.
	_, err = s.pool.Exec(ctx, `UPDATE runs SET changes = $1 WHERE id = $2 AND changes = $3::jsonb`, data, runID, string(stored))
	return err
}

// unitResourcesZones is unitResources that also gathers the state's
// Cloudflare zones into zones.
func (s *Store) unitResourcesZones(ctx context.Context, stackID, key string, zones reslabel.Zones) ([]api.StateResource, error) {
	data, err := s.stateBytes(ctx, stackID, key)
	if err != nil || data == nil {
		return nil, err
	}
	res, z, err := readState(data)
	if err != nil {
		return nil, err
	}
	maps.Copy(zones, z)
	return res, nil
}
