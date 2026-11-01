package store

import (
	"strings"
	"testing"
)

func TestListResourcesLabels(t *testing.T) {
	state := `{"resources":[
	 {"mode":"data","type":"cloudflare_zones","name":"this","provider":"provider[\"registry.terraform.io/cloudflare/cloudflare\"]",
	  "instances":[{"index_key":"acme.example","attributes":{"name":"acme.example","result":[{"id":"z1","name":"acme.example"}]}}]},
	 {"mode":"managed","type":"cloudflare_dns_record","name":"this","provider":"provider[\"registry.terraform.io/cloudflare/cloudflare\"]",
	  "instances":[
	   {"index_key":"acme.example|api|A|proxy1","attributes":{"zone_id":"z1","name":"api","type":"A","content":"203.0.113.10","proxied":true,"ttl":1,"comment":"keep out"}},
	   {"index_key":"acme.example|@|MX|mail","attributes":{"zone_id":"z1","name":"@","type":"MX","content":"mail.acme.example","priority":10,"ttl":300}},
	   {"index_key":"secret","attributes":{"zone_id":"z1","name":"vpn.acme.example","type":"A","content":"198.51.100.7"},
	    "sensitive_attributes":[[{"type":"get_attr","value":"content"}]]},
	   {"index_key":"odd","attributes":{"name":"x","content":"y"},"sensitive_attributes":"not a list"}
	  ]},
	 {"mode":"managed","type":"helm_release","name":"argocd","provider":"provider[\"registry.terraform.io/hashicorp/helm\"]",
	  "instances":[{"attributes":{"name":"argocd","chart":"argo-cd","version":"7.6.12","namespace":"platform","values":["password: hunter2"]}}]},
	 {"mode":"managed","type":"kubernetes_namespace","name":"platform","provider":"provider[\"registry.terraform.io/hashicorp/kubernetes\"]",
	  "instances":[{"attributes":{"metadata":[{"name":"platform","labels":{"a":"b"}}]}}]},
	 {"mode":"managed","type":"random_password","name":"db","provider":"provider[\"registry.terraform.io/hashicorp/random\"]",
	  "instances":[{"attributes":{"result":"hunter2"}}]}
	]}`
	res, err := listResources([]byte(state))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 8 {
		t.Fatalf("got %d resources", len(res))
	}
	if l := res[0].Label; l == nil || l.Name != "acme.example" {
		t.Errorf("zones data source: %+v", l)
	}
	api := res[1].Label
	if api == nil || api.Name != "api.acme.example" || api.Record == nil || api.Record.Type != "A" ||
		api.Record.Target != "203.0.113.10" || api.Record.Proxied == nil || !*api.Record.Proxied ||
		api.Record.TTL != 1 || api.Record.Zone != "acme.example" {
		t.Errorf("A record: %+v %+v", api, api.Record)
	}
	if mx := res[2].Label; mx == nil || mx.Name != "acme.example" || mx.Record.Target != "10 mail.acme.example" {
		t.Errorf("apex MX record: %+v", mx)
	}
	if v := res[3].Label; v == nil || v.Name != "vpn.acme.example" || v.Record.Target != "" {
		t.Errorf("a sensitive target must stay hidden: %+v %+v", v, v.Record)
	}
	if res[4].Label != nil {
		t.Errorf("an unknown sensitive format must read nothing: %+v", res[4].Label)
	}
	helm := res[5].Label
	if helm == nil || helm.Name != "argocd" || len(helm.Details) != 2 || helm.Details[0].Value != "argo-cd 7.6.12" || helm.Details[1].Value != "platform" {
		t.Errorf("helm release: %+v", helm)
	}
	if ns := res[6].Label; ns == nil || ns.Name != "platform" {
		t.Errorf("namespace: %+v", ns)
	}
	if res[7].Label != nil {
		t.Errorf("a type off the list gets no label: %+v", res[7].Label)
	}
	for _, r := range res {
		if r.Label == nil {
			continue
		}
		all := r.Label.Name
		for _, d := range r.Label.Details {
			all += d.Value
		}
		if strings.Contains(all, "hunter2") || strings.Contains(all, "keep out") {
			t.Errorf("%s leaks a value: %+v", r.Address, r.Label)
		}
	}
}
