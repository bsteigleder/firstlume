package executor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/reslabel"
)

const (
	// Limits that keep a huge plan reviewable and its upload bounded.
	maxAttributesPerResource = 100
	maxValueLength           = 300

	sensitiveValue = "(sensitive)"
	unknownValue   = "(known after apply)"
)

type planJSON struct {
	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Change  struct {
			Actions         []string `json:"actions"`
			Before          any      `json:"before"`
			After           any      `json:"after"`
			AfterUnknown    any      `json:"after_unknown"`
			BeforeSensitive any      `json:"before_sensitive"`
			AfterSensitive  any      `json:"after_sensitive"`
			ReplacePaths    [][]any  `json:"replace_paths"`
		} `json:"change"`
	} `json:"resource_changes"`
	PriorState struct {
		Values struct {
			RootModule stateModule `json:"root_module"`
		} `json:"values"`
	} `json:"prior_state"`
}

type stateModule struct {
	Resources []struct {
		Type            string `json:"type"`
		Values          any    `json:"values"`
		SensitiveValues any    `json:"sensitive_values"`
	} `json:"resources"`
	ChildModules []stateModule `json:"child_modules"`
}

// addZones records the Cloudflare zones a module and its children hold.
func (m stateModule) addZones(z reslabel.Zones) {
	for _, r := range m.Resources {
		if a, ok := reslabel.FromPlan(r.Values, r.SensitiveValues, nil); ok {
			z.Add(r.Type, a)
		}
	}
	for _, c := range m.ChildModules {
		c.addZones(z)
	}
}

// ExtractChanges lists what a plan (`show -json` output) does to each managed
// resource. Values Terraform marks sensitive become "(sensitive)", and mask
// hides any other secret that leaked into a value. unit names the Terragrunt
// unit in run --all plans ("" otherwise).
func ExtractChanges(raw []byte, unit string, mask func(string) string) ([]api.ResourceChange, error) {
	var p planJSON
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	zones := reslabel.Zones{}
	p.PriorState.Values.RootModule.addZones(zones)
	for _, rc := range p.ResourceChanges {
		if a, ok := reslabel.FromPlan(rc.Change.After, rc.Change.AfterSensitive, rc.Change.AfterUnknown); ok {
			zones.Add(rc.Type, a)
		}
	}
	out := []api.ResourceChange{}
	for _, rc := range p.ResourceChanges {
		action, ok := actionOf(rc.Change.Actions)
		if !ok || rc.Mode == "data" {
			continue
		}
		c := api.ResourceChange{Unit: unit, Address: rc.Address, Type: rc.Type, Name: rc.Name, Action: action}
		// A deleted resource is named by what it was, the others by what they will be.
		a, ok := reslabel.FromPlan(rc.Change.After, rc.Change.AfterSensitive, rc.Change.AfterUnknown)
		if action == api.ActionDelete {
			a, ok = reslabel.FromPlan(rc.Change.Before, rc.Change.BeforeSensitive, nil)
		}
		if ok {
			c.Label = maskLabel(reslabel.Label(rc.Mode, rc.Type, a, zones), mask)
		}
		if action != api.ActionDelete {
			before := map[string]string{}
			if action != api.ActionCreate {
				flatten(rc.Change.Before, rc.Change.BeforeSensitive, nil, "", before)
			}
			after := map[string]string{}
			flatten(rc.Change.After, rc.Change.AfterSensitive, rc.Change.AfterUnknown, "", after)

			var forcing []string
			for _, rp := range rc.Change.ReplacePaths {
				forcing = append(forcing, pathString(rp))
			}
			c.Attributes, c.Truncated = diff(before, after, forcing, mask)
		}
		out = append(out, c)
	}
	return out, nil
}

// maskLabel hides any secret that leaked into a label's values.
func maskLabel(l *api.ResourceLabel, mask func(string) string) *api.ResourceLabel {
	if l == nil || mask == nil {
		return l
	}
	l.Name = mask(l.Name)
	if l.Record != nil {
		l.Record.Target = mask(l.Record.Target)
	}
	for i := range l.Details {
		l.Details[i].Value = mask(l.Details[i].Value)
	}
	return l
}

func actionOf(actions []string) (api.ChangeAction, bool) {
	switch {
	case slices.Equal(actions, []string{"create"}):
		return api.ActionCreate, true
	case slices.Equal(actions, []string{"update"}):
		return api.ActionUpdate, true
	case slices.Equal(actions, []string{"delete"}):
		return api.ActionDelete, true
	case slices.Equal(actions, []string{"delete", "create"}), slices.Equal(actions, []string{"create", "delete"}):
		return api.ActionReplace, true
	}
	return "", false // no-op, read
}

// Summarize counts changes by action.
func Summarize(changes []api.ResourceChange) *api.PlanSummary {
	var s api.PlanSummary
	for _, c := range changes {
		switch c.Action {
		case api.ActionCreate:
			s.Add++
		case api.ActionUpdate:
			s.Change++
		case api.ActionReplace:
			s.Replace++
		case api.ActionDelete:
			s.Destroy++
		}
	}
	return &s
}

// flatten renders a JSON value into leaf paths ("tags.env", "ingress[0].cidr").
// sens and unknown mirror the value's shape with true where it is sensitive or
// not known until apply.
func flatten(v, sens, unknown any, path string, out map[string]string) {
	if sens == true {
		out[path] = sensitiveValue
		return
	}
	if unknown == true {
		out[path] = unknownValue
		return
	}
	switch x := v.(type) {
	case map[string]any:
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		if u, ok := unknown.(map[string]any); ok {
			for k := range u {
				keys[k] = true
			}
		}
		for k := range keys {
			flatten(x[k], child(sens, k), child(unknown, k), join(path, k), out)
		}
	case []any:
		for i, e := range x {
			flatten(e, index(sens, i), index(unknown, i), path+"["+strconv.Itoa(i)+"]", out)
		}
	case nil:
		if u, ok := unknown.(map[string]any); ok { // an object only known after apply
			for k, uv := range u {
				flatten(nil, child(sens, k), uv, join(path, k), out)
			}
		}
	case string:
		out[path] = x
	default:
		b, _ := json.Marshal(x)
		out[path] = string(b)
	}
}

func child(m any, k string) any {
	if mm, ok := m.(map[string]any); ok {
		return mm[k]
	}
	return m // true/false applies to everything below
}

func index(l any, i int) any {
	if ll, ok := l.([]any); ok {
		if i < len(ll) {
			return ll[i]
		}
		return nil
	}
	return l
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func pathString(p []any) string {
	var b strings.Builder
	for _, e := range p {
		switch x := e.(type) {
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(x)
		case float64:
			fmt.Fprintf(&b, "[%d]", int(x))
		}
	}
	return b.String()
}

func forces(path string, forcing []string) bool {
	for _, f := range forcing {
		if path == f || strings.HasPrefix(path, f+".") || strings.HasPrefix(path, f+"[") {
			return true
		}
	}
	return false
}

func diff(before, after map[string]string, forcing []string, mask func(string) string) ([]api.AttributeChange, bool) {
	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		b, inB := before[p]
		a, inA := after[p]
		if inB == inA && b == a && b != sensitiveValue {
			continue
		}
		if b == sensitiveValue && a == sensitiveValue && !forces(p, forcing) {
			continue // a sensitive value that may or may not change: nothing useful to show
		}
		sorted = append(sorted, p)
	}
	// Attributes forcing the replacement first, then alphabetically.
	slices.SortFunc(sorted, func(x, y string) int {
		fx, fy := forces(x, forcing), forces(y, forcing)
		if fx != fy {
			if fx {
				return -1
			}
			return 1
		}
		return strings.Compare(x, y)
	})
	truncated := len(sorted) > maxAttributesPerResource
	if truncated {
		sorted = sorted[:maxAttributesPerResource]
	}
	out := make([]api.AttributeChange, 0, len(sorted))
	for _, p := range sorted {
		ac := api.AttributeChange{Path: p, ForcesReplacement: forces(p, forcing)}
		if v, ok := before[p]; ok {
			ac.Before = render(v, mask)
		}
		if v, ok := after[p]; ok {
			ac.After = render(v, mask)
		}
		out = append(out, ac)
	}
	return out, truncated
}

func render(v string, mask func(string) string) *string {
	if mask != nil {
		v = mask(v)
	}
	if r := []rune(v); len(r) > maxValueLength {
		v = string(r[:maxValueLength]) + "…"
	}
	return &v
}
