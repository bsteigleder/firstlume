// Package reslabel says which resource a state or plan entry is in plain
// words (a DNS record's name, type and target; a Helm release's name), from
// a fixed list of identifying attributes per type. No other attribute value,
// and no attribute marked sensitive, is ever read.
package reslabel

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bsteigleder/firstlume/internal/api"
)

// maxLen bounds a label field's length; longer values are cut with "…".
const maxLen = 120

// Attrs are an instance's attributes as the labels may read them: hidden
// ones (sensitive, or unknown until apply) read as absent.
type Attrs struct {
	raw    map[string]json.RawMessage
	hidden map[string]bool
}

// FromState reads a state instance's attributes and sensitive_attributes.
// ok is false for a sensitive_attributes format it does not know, and then
// nothing may be read.
func FromState(attrs map[string]json.RawMessage, sensitive json.RawMessage) (a Attrs, ok bool) {
	a = Attrs{raw: attrs, hidden: map[string]bool{}}
	if len(sensitive) == 0 || string(sensitive) == "null" {
		return a, true
	}
	// Each sensitive path is a list of steps; the first names a top-level attribute.
	var paths [][]struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(sensitive, &paths); err != nil {
		return a, false
	}
	for _, p := range paths {
		if len(p) == 0 {
			continue
		}
		var name string
		if json.Unmarshal(p[0].Value, &name) == nil {
			a.hidden[name] = true
		}
	}
	return a, true
}

// FromPlan reads a planned value from `show -json`. sensitive and unknown
// mirror its shape; an attribute with anything sensitive or unknown inside
// it is hidden whole.
func FromPlan(value, sensitive, unknown any) (a Attrs, ok bool) {
	values, isMap := value.(map[string]any)
	if !isMap || sensitive == true || unknown == true {
		return a, false
	}
	a = Attrs{raw: map[string]json.RawMessage{}, hidden: map[string]bool{}}
	for k, v := range values {
		if marked(sensitive, k) || marked(unknown, k) {
			a.hidden[k] = true
			continue
		}
		if b, err := json.Marshal(v); err == nil {
			a.raw[k] = b
		}
	}
	return a, true
}

// FromChange reads the values a stored plan change was made with, for runs
// planned before changes carried labels. Only top-level attributes count;
// sensitive and unknown ones are absent.
func FromChange(attrs []api.AttributeChange) Attrs {
	a := Attrs{raw: map[string]json.RawMessage{}, hidden: map[string]bool{}}
	for _, at := range attrs {
		if at.After == nil || strings.ContainsAny(at.Path, ".[") {
			continue
		}
		if v := *at.After; v != sensitiveValue && v != unknownValue {
			a.raw[at.Path], _ = json.Marshal(v)
		}
	}
	return a
}

// The executor's renderings of values it does not show.
const (
	sensitiveValue = "(sensitive)"
	unknownValue   = "(known after apply)"
)

// Known reports whether a managed resource type has a label.
func Known(typ string) bool {
	switch typ {
	case "cloudflare_dns_record", "cloudflare_record", "cloudflare_zone", "helm_release", "aws_s3_bucket",
		"aws_eks_cluster", "aws_iam_role", "lxd_instance", "aws_db_instance", "kubernetes_namespace", "kubernetes_namespace_v1":
		return true
	}
	return false
}

// marked reports whether a shape map marks key, or anything inside it.
func marked(shape any, key string) bool {
	m, ok := shape.(map[string]any)
	if !ok {
		return false
	}
	return any_(m[key])
}

func any_(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case map[string]any:
		for _, e := range x {
			if any_(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if any_(e) {
				return true
			}
		}
	}
	return false
}

func (a Attrs) str(name string) string {
	if a.hidden[name] {
		return ""
	}
	var s string
	if json.Unmarshal(a.raw[name], &s) != nil {
		return ""
	}
	return cut(s)
}

func (a Attrs) num(name string) int {
	if a.hidden[name] {
		return 0
	}
	var n float64
	if json.Unmarshal(a.raw[name], &n) != nil {
		var s string // a stored plan change keeps numbers as text
		if json.Unmarshal(a.raw[name], &s) != nil {
			return 0
		}
		n, _ = strconv.ParseFloat(s, 64)
	}
	return int(n)
}

func (a Attrs) boolean(name string) *bool {
	if a.hidden[name] {
		return nil
	}
	var b bool
	if json.Unmarshal(a.raw[name], &b) != nil {
		var s string // a stored plan change keeps booleans as text
		if json.Unmarshal(a.raw[name], &s) != nil || (s != "true" && s != "false") {
			return nil
		}
		b = s == "true"
	}
	return &b
}

// firstBlock reads a string field of the first nested block, such as
// metadata[0].name.
func (a Attrs) firstBlock(block, name string) string {
	if a.hidden[block] {
		return ""
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(a.raw[block], &blocks) != nil || len(blocks) == 0 {
		return ""
	}
	return Attrs{raw: blocks[0]}.str(name)
}

func cut(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return string([]rune(s)[:maxLen-1]) + "…"
}

// Zones maps Cloudflare zone ids to names, from the zones a state or plan
// manages or reads, so a record named relative to its zone shows in full.
type Zones map[string]string

// Add records the zones a resource or data source of typ holds.
func (z Zones) Add(typ string, a Attrs) {
	switch typ {
	case "cloudflare_zone":
		name := a.str("name")
		if name == "" {
			name = a.str("zone") // provider v4
		}
		for _, id := range []string{a.str("id"), a.str("zone_id")} {
			if id != "" && name != "" {
				z[id] = name
			}
		}
	case "cloudflare_zones":
		for _, list := range []string{"result", "zones"} { // v5, v4
			if a.hidden[list] {
				continue
			}
			var zones []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(a.raw[list], &zones) != nil {
				continue
			}
			for _, zn := range zones {
				if zn.ID != "" && zn.Name != "" {
					z[zn.ID] = cut(zn.Name)
				}
			}
		}
	}
}

// Label returns what identifies a resource of a known type, or nil. mode is
// "managed" or "data".
func Label(mode, typ string, a Attrs, zones Zones) *api.ResourceLabel {
	named := func(name string, details ...api.LabelDetail) *api.ResourceLabel {
		if name == "" {
			return nil
		}
		var kept []api.LabelDetail
		for _, d := range details {
			if d.Value != "" {
				kept = append(kept, d)
			}
		}
		return &api.ResourceLabel{Name: name, Details: kept}
	}
	if mode == "data" {
		switch typ {
		case "cloudflare_zone", "cloudflare_zones":
			return named(a.str("name"))
		}
		return nil
	}
	switch typ {
	case "cloudflare_dns_record", "cloudflare_record":
		return dnsLabel(a, zones)
	case "cloudflare_zone":
		name := a.str("name")
		if name == "" {
			name = a.str("zone")
		}
		return named(name)
	case "helm_release":
		return named(a.str("name"),
			api.LabelDetail{Key: "chart", Value: strings.TrimSpace(a.str("chart") + " " + a.str("version"))},
			api.LabelDetail{Key: "namespace", Value: a.str("namespace")})
	case "aws_s3_bucket":
		return named(a.str("bucket"))
	case "aws_eks_cluster", "aws_iam_role", "lxd_instance":
		return named(a.str("name"))
	case "aws_db_instance":
		return named(a.str("identifier"))
	case "kubernetes_namespace", "kubernetes_namespace_v1":
		return named(a.firstBlock("metadata", "name"))
	}
	return nil
}

func dnsLabel(a Attrs, zones Zones) *api.ResourceLabel {
	zone := zones[a.str("zone_id")]
	name := a.str("hostname") // provider v4 keeps the full name here
	if name == "" {
		name = a.str("name")
	}
	switch {
	case name == "" && zone == "":
		return nil
	case name == "" || name == "@":
		name = zone
	case zone != "" && name != zone && !strings.HasSuffix(name, "."+zone):
		name += "." + zone
	}
	target := a.str("content")
	if target == "" {
		target = a.str("value") // provider v4
	}
	if p := a.num("priority"); p > 0 && target != "" {
		target = strconv.Itoa(p) + " " + target
	}
	return &api.ResourceLabel{Name: name, Record: &api.DNSRecord{
		Type: a.str("type"), Target: target, Proxied: a.boolean("proxied"), TTL: a.num("ttl"), Zone: zone,
	}}
}
