package reslabel

import (
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

func TestCut(t *testing.T) {
	got := cut(strings.Repeat("é", 200))
	if n := len([]rune(got)); n != maxLen || !strings.HasSuffix(got, "…") {
		t.Errorf("got %d runes", n)
	}
}

func TestFromPlanHidesSensitiveAndUnknown(t *testing.T) {
	after := map[string]any{"name": "api", "zone_id": "z1", "type": "A", "content": "203.0.113.10", "proxied": true,
		"tags": []any{"x"}, "id": nil}
	sensitive := map[string]any{"content": true, "tags": []any{false}}
	unknown := map[string]any{"id": true, "proxied": false}
	a, ok := FromPlan(after, sensitive, unknown)
	if !ok {
		t.Fatal("not read")
	}
	l := Label("managed", "cloudflare_dns_record", a, Zones{"z1": "acme.example"})
	if l == nil || l.Name != "api.acme.example" || l.Record.Target != "" || l.Record.Proxied == nil || !*l.Record.Proxied {
		t.Fatalf("label: %+v %+v", l, l.Record)
	}
	if _, ok := FromPlan(after, true, nil); ok {
		t.Error("a wholly sensitive value must read nothing")
	}
	if _, ok := FromPlan(nil, nil, nil); ok {
		t.Error("a deleted value has nothing to read")
	}
}

func TestFromChange(t *testing.T) {
	s := func(v string) *string { return &v }
	a := FromChange([]api.AttributeChange{
		{Path: "name", After: s("firstlume")}, {Path: "zone_id", After: s("z1")}, {Path: "type", After: s("A")},
		{Path: "content", After: s("(sensitive)")}, {Path: "proxied", After: s("true")}, {Path: "ttl", After: s("1")},
		{Path: "id", After: s("(known after apply)")}, {Path: "tags[0]", After: s("x")},
	})
	l := Label("managed", "cloudflare_dns_record", a, Zones{"z1": "acme.example"})
	if l == nil || l.Name != "firstlume.acme.example" || l.Record.Type != "A" || l.Record.Target != "" ||
		l.Record.Proxied == nil || !*l.Record.Proxied || l.Record.TTL != 1 {
		t.Fatalf("label: %+v %+v", l, l.Record)
	}
	if !Known("cloudflare_dns_record") || Known("random_password") {
		t.Error("Known")
	}
}
