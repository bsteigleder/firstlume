package executor

import (
	"encoding/json"

	"github.com/bsteigleder/firstlume/internal/api"
)

type planJSON struct {
	ResourceChanges []struct {
		Change struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// SummarizePlan counts changes from `show -json` output the same way Terraform's
// "Plan: X to add, Y to change, Z to destroy" line does (a replace is add+destroy).
func SummarizePlan(raw []byte) (*api.PlanSummary, error) {
	var p planJSON
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	var s api.PlanSummary
	for _, rc := range p.ResourceChanges {
		for _, a := range rc.Change.Actions {
			switch a {
			case "create":
				s.Add++
			case "update":
				s.Change++
			case "delete":
				s.Destroy++
			}
		}
	}
	return &s, nil
}
