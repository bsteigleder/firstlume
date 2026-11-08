package api

import (
	"bytes"
	"encoding/json"
)

// Placement says where a run's Job goes (spec 0007). Each field holds the JSON
// of the Kubernetes field of the same name. An absent field inherits; an empty
// one ({} or []) cancels what is inherited, the installation's value included.
type Placement struct {
	NodeSelector    json.RawMessage `json:"node_selector,omitempty"`
	Tolerations     json.RawMessage `json:"tolerations,omitempty"`
	NodeAffinity    json.RawMessage `json:"node_affinity,omitempty"`
	PodAffinity     json.RawMessage `json:"pod_affinity,omitempty"`
	PodAntiAffinity json.RawMessage `json:"pod_anti_affinity,omitempty"`
}

// PlacementFieldNames are the fields in display order, by their JSON names.
var PlacementFieldNames = []string{"node_selector", "tolerations", "node_affinity", "pod_affinity", "pod_anti_affinity"}

// Field returns a pointer to the named field.
func (p *Placement) Field(name string) *json.RawMessage {
	switch name {
	case "node_selector":
		return &p.NodeSelector
	case "tolerations":
		return &p.Tolerations
	case "node_affinity":
		return &p.NodeAffinity
	case "pod_affinity":
		return &p.PodAffinity
	case "pod_anti_affinity":
		return &p.PodAntiAffinity
	}
	return nil
}

// IsZero says the placement defines nothing.
func (p Placement) IsZero() bool {
	for _, name := range PlacementFieldNames {
		if len(*p.Field(name)) > 0 {
			return false
		}
	}
	return true
}

// Normalize makes a JSON null the same as an absent field.
func (p *Placement) Normalize() {
	for _, name := range PlacementFieldNames {
		if f := p.Field(name); bytes.Equal(bytes.TrimSpace(*f), []byte("null")) {
			*f = nil
		}
	}
}

// Over returns p with the fields it doesn't define taken from base.
func (p Placement) Over(base Placement) Placement {
	for _, name := range PlacementFieldNames {
		if f := p.Field(name); len(*f) == 0 {
			*f = *base.Field(name)
		}
	}
	return p
}

// PlacementField is a field's value and the level it came from.
type PlacementField struct {
	Value  json.RawMessage `json:"value"`
	Origin Level           `json:"origin"`
}

// EffectivePlacement is what the organization, folders and stack define, once
// inheritance is resolved. The agent and the installation come after it.
type EffectivePlacement struct {
	NodeSelector    *PlacementField `json:"node_selector,omitempty"`
	Tolerations     *PlacementField `json:"tolerations,omitempty"`
	NodeAffinity    *PlacementField `json:"node_affinity,omitempty"`
	PodAffinity     *PlacementField `json:"pod_affinity,omitempty"`
	PodAntiAffinity *PlacementField `json:"pod_anti_affinity,omitempty"`
}

// Field returns a pointer to the named field.
func (e *EffectivePlacement) Field(name string) **PlacementField {
	switch name {
	case "node_selector":
		return &e.NodeSelector
	case "tolerations":
		return &e.Tolerations
	case "node_affinity":
		return &e.NodeAffinity
	case "pod_affinity":
		return &e.PodAffinity
	case "pod_anti_affinity":
		return &e.PodAntiAffinity
	}
	return nil
}

// Values drops the origins.
func (e EffectivePlacement) Values() Placement {
	var p Placement
	for _, name := range PlacementFieldNames {
		if f := *e.Field(name); f != nil {
			*p.Field(name) = f.Value
		}
	}
	return p
}

// ClaimRequest is what an agent says about itself when it asks for work.
type ClaimRequest struct {
	// AcceptsPlacement is agent.executor.allowPlacement (R3.1).
	AcceptsPlacement bool `json:"accepts_placement"`
	// InstallPlacement is what the Helm values set (R5.2).
	InstallPlacement Placement `json:"install_placement"`
	// AcceptsCommands says the agent can take command runs (spec 0009, E1):
	// the Kubernetes driver, which labels their pods for the egress policy,
	// or the local driver with FIRSTLUME_ALLOW_LOCAL_COMMANDS for development.
	// Agents from before command runs send nothing and never get one.
	AcceptsCommands bool `json:"accepts_commands"`
}
