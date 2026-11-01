package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bsteigleder/firstlume/internal/api"
)

// maxLabel bounds a label field's length; longer values are cut with "…".
const maxLabel = 120

// labelAttrs reads an instance's identifying attributes: only the ones a
// labeler asks for, and never one the state marks sensitive.
type labelAttrs struct {
	raw       map[string]json.RawMessage
	sensitive map[string]bool
}

func newLabelAttrs(attrs map[string]json.RawMessage, sensitive json.RawMessage) (labelAttrs, bool) {
	a := labelAttrs{raw: attrs, sensitive: map[string]bool{}}
	if len(sensitive) == 0 || string(sensitive) == "null" {
		return a, true
	}
	// Each sensitive path is a list of steps; the first names a top-level attribute.
	var paths [][]struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(sensitive, &paths); err != nil {
		return a, false // an unknown format: read nothing
	}
	for _, p := range paths {
		if len(p) == 0 {
			continue
		}
		var name string
		if json.Unmarshal(p[0].Value, &name) == nil {
			a.sensitive[name] = true
		}
	}
	return a, true
}

func (a labelAttrs) str(name string) string {
	if a.sensitive[name] {
		return ""
	}
	var s string
	if json.Unmarshal(a.raw[name], &s) != nil {
		return ""
	}
	return cut(s)
}

func (a labelAttrs) num(name string) int {
	if a.sensitive[name] {
		return 0
	}
	var n float64
	if json.Unmarshal(a.raw[name], &n) != nil {
		return 0
	}
	return int(n)
}

func (a labelAttrs) boolean(name string) *bool {
	if a.sensitive[name] {
		return nil
	}
	var b bool
	if json.Unmarshal(a.raw[name], &b) != nil {
		return nil
	}
	return &b
}

// firstBlock reads a string field of the first nested block, such as
// metadata[0].name.
func (a labelAttrs) firstBlock(block, name string) string {
	if a.sensitive[block] {
		return ""
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(a.raw[block], &blocks) != nil || len(blocks) == 0 {
		return ""
	}
	return labelAttrs{raw: blocks[0]}.str(name)
}

func cut(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxLabel {
		return s
	}
	return string([]rune(s)[:maxLabel-1]) + "…"
}

// zoneNames maps Cloudflare zone ids to names, from the zones a state
// manages or reads, so a record named relative to its zone shows in full.
type zoneNames map[string]string

func (z zoneNames) add(typ string, a labelAttrs) {
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
			if a.sensitive[list] {
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

// resourceLabel returns what identifies a resource of a known type, or nil.
func resourceLabel(mode, typ string, a labelAttrs, zones zoneNames) *api.ResourceLabel {
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

func dnsLabel(a labelAttrs, zones zoneNames) *api.ResourceLabel {
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
