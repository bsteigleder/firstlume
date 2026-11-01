package store

import (
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

func TestDeriveApply(t *testing.T) {
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	at := func(s int) *time.Time { v := t0.Add(time.Duration(s) * time.Second); return &v }
	step := func(a api.ChangeAction, start, end int, failed bool) api.ApplyStep {
		st := api.ApplyStep{Action: a, StartedAt: at(start), Failed: failed}
		if end >= 0 {
			st.FinishedAt = at(end)
		}
		return st
	}
	cases := []struct {
		name     string
		action   api.ChangeAction
		rec      *applyRecord
		finished bool
		want     api.ApplyState
		duration int64
		errors   int
	}{
		{"not started yet", api.ActionCreate, nil, false, api.ApplyPending, 0, 0},
		{"never started", api.ActionCreate, nil, true, api.ApplyNotApplied, 0, 0},
		{"creating", api.ActionCreate, &applyRecord{Steps: []api.ApplyStep{step(api.ActionCreate, 0, -1, false)}}, false, api.ApplyApplying, 0, 0},
		{"created", api.ActionCreate, &applyRecord{Steps: []api.ApplyStep{step(api.ActionCreate, 0, 3, false)}}, false, api.ApplyDone, 3000, 0},
		{"replace half way", api.ActionReplace, &applyRecord{Steps: []api.ApplyStep{step(api.ActionDelete, 0, 2, false)}}, false, api.ApplyApplying, 0, 0},
		{"replaced", api.ActionReplace, &applyRecord{Steps: []api.ApplyStep{
			step(api.ActionDelete, 0, 2, false), step(api.ActionCreate, 2, 9, false)}}, true, api.ApplyDone, 9000, 0},
		{"create failed", api.ActionCreate, &applyRecord{Steps: []api.ApplyStep{step(api.ActionCreate, 0, 5, true)},
			Errors: []api.ApplyError{{Summary: "boom"}}}, true, api.ApplyFailed, 5000, 1},
		{"error without a failed step", api.ActionUpdate, &applyRecord{Errors: []api.ApplyError{{Summary: "boom"}}}, false, api.ApplyFailed, 0, 1},
		{"executor died mid-create", api.ActionCreate, &applyRecord{Steps: []api.ApplyStep{step(api.ActionCreate, 0, -1, false)}}, true, api.ApplyFailed, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveApply(c.action, c.rec, c.finished)
			if got.State != c.want || got.DurationMS != c.duration || len(got.Errors) != c.errors {
				t.Fatalf("got %+v, want state %s, duration %d, %d errors", got, c.want, c.duration, c.errors)
			}
			if got.State == api.ApplyApplying && got.FinishedAt != nil {
				t.Fatalf("a running resource has no end: %+v", got)
			}
		})
	}
	died := deriveApply(api.ActionCreate, &applyRecord{Steps: []api.ApplyStep{step(api.ActionCreate, 0, -1, false)}}, true)
	if died.Errors[0].Summary != endedEarly {
		t.Fatalf("died: %+v", died.Errors)
	}
}
