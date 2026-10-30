package api

import "testing"

func TestStatusAfterPlan(t *testing.T) {
	cases := []struct {
		typ         RunType
		changes     bool
		autoApprove bool
		want        RunStatus
	}{
		{RunPlan, false, false, StatusNoChanges},
		{RunPlan, true, false, StatusPlanned},
		{RunPlan, true, true, StatusPlanned},
		{RunApply, false, false, StatusNoChanges},
		{RunApply, true, false, StatusAwaitingApproval},
		{RunApply, true, true, StatusApplyQueued},
		{RunDestroy, true, false, StatusAwaitingApproval},
		{RunDestroy, true, true, StatusApplyQueued},
	}
	for _, c := range cases {
		if got := StatusAfterPlan(c.typ, c.changes, c.autoApprove); got != c.want {
			t.Errorf("StatusAfterPlan(%s, changes=%v, auto=%v) = %s, want %s", c.typ, c.changes, c.autoApprove, got, c.want)
		}
	}
}

func TestTerminalStatuses(t *testing.T) {
	for _, s := range []RunStatus{StatusQueued, StatusPlanning, StatusAwaitingApproval, StatusApplyQueued, StatusApplying} {
		if s.IsTerminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	for _, s := range []RunStatus{StatusPlanned, StatusNoChanges, StatusApplied, StatusFailed, StatusDiscarded, StatusCanceled} {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
}
