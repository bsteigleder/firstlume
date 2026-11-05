// Package placement reads and validates where run Jobs go (spec 0007), using
// the Kubernetes types, so a typo never silently puts a run on any node.
package placement

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/bsteigleder/firstlume/internal/api"
)

// maxSize limits a level's placement, all fields together.
const maxSize = 16 << 10

// fieldLabels name the fields in errors, as the screens do.
var fieldLabels = map[string]string{
	"node_selector":     "Node selector",
	"tolerations":       "Tolerations",
	"node_affinity":     "Node affinity",
	"pod_affinity":      "Pod affinity",
	"pod_anti_affinity": "Pod anti-affinity",
}

// Parsed holds the fields of a Placement that are set, as Kubernetes types.
type Parsed struct {
	NodeSelector    map[string]string
	Tolerations     []corev1.Toleration
	NodeAffinity    *corev1.NodeAffinity
	PodAffinity     *corev1.PodAffinity
	PodAntiAffinity *corev1.PodAntiAffinity
}

func decode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected content after the value")
	}
	return nil
}

// Parse reads the fields that are set. Unknown fields are an error.
func Parse(p api.Placement) (*Parsed, error) {
	out := &Parsed{}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
		into any
	}{
		{"node_selector", p.NodeSelector, &out.NodeSelector},
		{"tolerations", p.Tolerations, &out.Tolerations},
		{"node_affinity", p.NodeAffinity, &out.NodeAffinity},
		{"pod_affinity", p.PodAffinity, &out.PodAffinity},
		{"pod_anti_affinity", p.PodAntiAffinity, &out.PodAntiAffinity},
	} {
		if len(f.raw) == 0 {
			continue
		}
		if err := decode(f.raw, f.into); err != nil {
			return nil, fmt.Errorf("%s: %w", fieldLabels[f.name], err)
		}
	}
	return out, nil
}

// Validate checks a placement before it is saved. It catches what can be known
// without a cluster; anything else comes back from the API server as a failed
// run, with its message (R1.5).
func Validate(p api.Placement) error {
	size := 0
	for _, name := range api.PlacementFieldNames {
		size += len(*p.Field(name))
	}
	if size > maxSize {
		return fmt.Errorf("placement can't be larger than 16 KB")
	}
	parsed, err := Parse(p)
	if err != nil {
		return err
	}
	for k, v := range parsed.NodeSelector {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("Node selector: invalid label %q: %s", k, errs[0])
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("Node selector: invalid value %q for %s: %s", v, k, errs[0])
		}
	}
	for i, t := range parsed.Tolerations {
		if err := validToleration(t); err != nil {
			return fmt.Errorf("Tolerations: item %d: %w", i+1, err)
		}
	}
	if err := validNodeAffinity(parsed.NodeAffinity); err != nil {
		return fmt.Errorf("Node affinity: %w", err)
	}
	if a := parsed.PodAffinity; a != nil {
		if err := validPodTerms(a.RequiredDuringSchedulingIgnoredDuringExecution, a.PreferredDuringSchedulingIgnoredDuringExecution); err != nil {
			return fmt.Errorf("Pod affinity: %w", err)
		}
	}
	if a := parsed.PodAntiAffinity; a != nil {
		if err := validPodTerms(a.RequiredDuringSchedulingIgnoredDuringExecution, a.PreferredDuringSchedulingIgnoredDuringExecution); err != nil {
			return fmt.Errorf("Pod anti-affinity: %w", err)
		}
	}
	return nil
}

func validToleration(t corev1.Toleration) error {
	if t.Key != "" {
		if errs := validation.IsQualifiedName(t.Key); len(errs) > 0 {
			return fmt.Errorf("invalid key %q: %s", t.Key, errs[0])
		}
	}
	switch t.Operator {
	case "", corev1.TolerationOpEqual:
		if t.Key == "" {
			return fmt.Errorf("key is required unless the operator is Exists")
		}
	case corev1.TolerationOpExists:
		if t.Value != "" {
			return fmt.Errorf("value must be empty when the operator is Exists")
		}
	default:
		return fmt.Errorf("operator must be Equal or Exists")
	}
	switch t.Effect {
	case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return fmt.Errorf("effect must be NoSchedule, PreferNoSchedule or NoExecute")
	}
	if t.TolerationSeconds != nil && t.Effect != corev1.TaintEffectNoExecute {
		return fmt.Errorf("tolerationSeconds only applies to NoExecute")
	}
	return nil
}

func validNodeAffinity(a *corev1.NodeAffinity) error {
	if a == nil {
		return nil
	}
	if req := a.RequiredDuringSchedulingIgnoredDuringExecution; req != nil {
		if len(req.NodeSelectorTerms) == 0 {
			return fmt.Errorf("nodeSelectorTerms can't be empty in the required rule")
		}
		for _, term := range req.NodeSelectorTerms {
			if err := validNodeTerm(term); err != nil {
				return err
			}
		}
	}
	for _, pref := range a.PreferredDuringSchedulingIgnoredDuringExecution {
		if err := validWeight(pref.Weight); err != nil {
			return err
		}
		if err := validNodeTerm(pref.Preference); err != nil {
			return err
		}
	}
	return nil
}

func validNodeTerm(term corev1.NodeSelectorTerm) error {
	if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
		return fmt.Errorf("a term needs matchExpressions or matchFields")
	}
	for _, e := range append(append([]corev1.NodeSelectorRequirement{}, term.MatchExpressions...), term.MatchFields...) {
		if e.Key == "" {
			return fmt.Errorf("key is required in each match expression")
		}
		switch e.Operator {
		case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
			if len(e.Values) == 0 {
				return fmt.Errorf("%s needs values, for key %s", e.Operator, e.Key)
			}
		case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
			if len(e.Values) > 0 {
				return fmt.Errorf("%s takes no values, for key %s", e.Operator, e.Key)
			}
		case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			if len(e.Values) != 1 {
				return fmt.Errorf("%s needs a single value, for key %s", e.Operator, e.Key)
			}
		default:
			return fmt.Errorf("operator must be one of %s, for key %s",
				strings.Join([]string{"In", "NotIn", "Exists", "DoesNotExist", "Gt", "Lt"}, ", "), e.Key)
		}
	}
	return nil
}

func validPodTerms(required []corev1.PodAffinityTerm, preferred []corev1.WeightedPodAffinityTerm) error {
	for _, term := range required {
		if err := validPodTerm(term); err != nil {
			return err
		}
	}
	for _, pref := range preferred {
		if err := validWeight(pref.Weight); err != nil {
			return err
		}
		if err := validPodTerm(pref.PodAffinityTerm); err != nil {
			return err
		}
	}
	return nil
}

func validPodTerm(term corev1.PodAffinityTerm) error {
	if term.TopologyKey == "" {
		return fmt.Errorf("topologyKey is required in podAffinityTerm")
	}
	if errs := validation.IsQualifiedName(term.TopologyKey); len(errs) > 0 {
		return fmt.Errorf("invalid topologyKey %q: %s", term.TopologyKey, errs[0])
	}
	if _, err := metav1.LabelSelectorAsSelector(term.LabelSelector); err != nil {
		return fmt.Errorf("labelSelector: %w", err)
	}
	return nil
}

func validWeight(w int32) error {
	if w < 1 || w > 100 {
		return fmt.Errorf("weight must be between 1 and 100")
	}
	return nil
}
