package placement

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

func TestValidate(t *testing.T) {
	antiAffinity := func(term string) json.RawMessage {
		return json.RawMessage(`{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":100,"podAffinityTerm":` + term + `}]}`)
	}
	good := []api.Placement{
		{},
		{NodeSelector: json.RawMessage(`{"topology.kubernetes.io/zone":"east"}`)},
		{NodeSelector: json.RawMessage(`{}`), Tolerations: json.RawMessage(`[]`), PodAntiAffinity: json.RawMessage(`{}`)},
		{Tolerations: json.RawMessage(`[{"key":"dedicated","operator":"Equal","value":"iac","effect":"NoSchedule"}]`)},
		{Tolerations: json.RawMessage(`[{"operator":"Exists"}]`)},
		{NodeAffinity: json.RawMessage(`{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"zone","operator":"In","values":["east"]}]}]}}`)},
		{PodAntiAffinity: antiAffinity(`{"topologyKey":"kubernetes.io/hostname","labelSelector":{"matchLabels":{"app.kubernetes.io/managed-by":"firstlume"}}}`)},
	}
	for _, p := range good {
		if err := Validate(p); err != nil {
			t.Errorf("Validate(%+v) = %v", p, err)
		}
	}

	bad := []struct {
		p    api.Placement
		want string
	}{
		{api.Placement{NodeSelector: json.RawMessage(`["east"]`)}, "Node selector"},
		{api.Placement{NodeSelector: json.RawMessage(`{"bad key":"east"}`)}, "invalid label"},
		{api.Placement{NodeSelector: json.RawMessage(`{"zone":"not a value"}`)}, "invalid value"},
		{api.Placement{Tolerations: json.RawMessage(`[{"key":"a","operator":"Maybe"}]`)}, "operator must be"},
		{api.Placement{Tolerations: json.RawMessage(`[{"key":"a","effect":"Never"}]`)}, "effect must be"},
		{api.Placement{Tolerations: json.RawMessage(`[{"key":"a","operator":"Exists","value":"b"}]`)}, "value must be empty"},
		{api.Placement{Tolerations: json.RawMessage(`[{"value":"b"}]`)}, "key is required"},
		{api.Placement{Tolerations: json.RawMessage(`[{"key":"a","tolerationSeconds":5}]`)}, "tolerationSeconds"},
		{api.Placement{NodeAffinity: json.RawMessage(`{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[]}}`)}, "nodeSelectorTerms"},
		{api.Placement{NodeAffinity: json.RawMessage(`{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"z","operator":"In"}]}]}}`)}, "needs values"},
		{api.Placement{NodeAffinity: json.RawMessage(`{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":500,"preference":{"matchExpressions":[{"key":"z","operator":"Exists"}]}}]}`)}, "weight"},
		{api.Placement{PodAffinity: json.RawMessage(`{"requiredDuringSchedulingIgnoredDuringExecution":[{"labelSelector":{}}]}`)}, "Pod affinity: topologyKey is required"},
		{api.Placement{PodAntiAffinity: antiAffinity(`{"labelSelector":{}}`)}, "Pod anti-affinity: topologyKey is required"},
		// A typo must not silently drop the constraint.
		{api.Placement{PodAntiAffinity: json.RawMessage(`{"preferedDuringScheduling":[]}`)}, "unknown field"},
		{api.Placement{NodeSelector: json.RawMessage(`{"z":"` + strings.Repeat("a", 20000) + `"}`)}, "16 KB"},
	}
	for _, c := range bad {
		err := Validate(c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%.80s) = %v, want %q", string(c.p.NodeSelector)+string(c.p.Tolerations)+string(c.p.NodeAffinity)+string(c.p.PodAffinity)+string(c.p.PodAntiAffinity), err, c.want)
		}
	}
}

func TestOverAndNormalize(t *testing.T) {
	run := api.Placement{NodeSelector: json.RawMessage(`{"zone":"east"}`), Tolerations: json.RawMessage(`[]`)}
	agent := api.Placement{NodeSelector: json.RawMessage(`{"zone":"west"}`), Tolerations: json.RawMessage(`[{"operator":"Exists"}]`), PodAffinity: json.RawMessage(`{}`)}
	got := run.Over(agent)
	if string(got.NodeSelector) != `{"zone":"east"}` || string(got.Tolerations) != `[]` || string(got.PodAffinity) != `{}` {
		t.Fatalf("Over = %+v", got)
	}
	p := api.Placement{NodeSelector: json.RawMessage(`null`)}
	p.Normalize()
	if !p.IsZero() {
		t.Fatalf("null should count as absent: %+v", p)
	}
}
