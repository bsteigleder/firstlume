package stateid

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

func unit(key string, addresses ...string) Unit {
	return Unit{Key: key, Addresses: set(addresses...), Literals: map[string]bool{}}
}

func state(source, dir string, addresses ...string) State {
	return State{Source: source, Dir: dir, Lineage: "lin-" + source, Resources: len(addresses), Addresses: addresses}
}

func one(t *testing.T, st State, units []Unit) Result {
	t.Helper()
	return Identify([]State{st}, units, nil, "environments/prod")[0]
}

func TestByPath(t *testing.T) {
	units := []Unit{unit("network", "aws_vpc.main"), unit("k8s/addons", "helm_release.a"), unit("addons", "helm_release.b")}
	r := one(t, state("a", "infra/environments/prod/k8s/addons", "helm_release.a"), units)
	if !r.Placed || r.Unit != "k8s/addons" || r.Reason != ReasonPath {
		t.Fatalf("the longest path wins: %+v", r)
	}
	st := state("network.tfstate", "", "aws_vpc.main")
	st.Stem = "network"
	if r := one(t, st, units); !r.Placed || r.Unit != "network" || r.Reason != ReasonPath {
		t.Fatalf("a file named like the folder: %+v", r)
	}
}

func TestByResources(t *testing.T) {
	units := []Unit{unit("network", "aws_vpc.main", "aws_subnet.a"), unit("vault", "vault_mount.kv"), unit("big", "aws_vpc.main", "aws_subnet.a", "aws_s3_bucket.x", "aws_s3_bucket.y")}
	r := one(t, state("old.tfstate", "", "aws_vpc.main", "aws_subnet.a"), units)
	if !r.Placed || r.Unit != "network" || r.Reason != ReasonResources || r.Matched != 2 || r.Total != 2 {
		t.Fatalf("the closest folder wins over a bigger one holding the same names: %+v", r)
	}
}

// CAS5.3: a path that matches doesn't win over code that says otherwise.
func TestPathContradictedByCode(t *testing.T) {
	units := []Unit{unit("network", "aws_vpc.main"), unit("vault", "vault_mount.kv")}
	r := one(t, state("a", "x/network", "vault_mount.kv"), units)
	if !r.Placed || r.Unit != "vault" || r.Reason != ReasonResources {
		t.Fatalf("the resources point elsewhere: %+v", r)
	}
	r = one(t, state("b", "x/network", "aws_iam_role.other"), units)
	if r.Placed || r.Why != WhyContradicts || !reflect.DeepEqual(r.Candidates, []string{"network"}) {
		t.Fatalf("nothing else fits: %+v", r)
	}
}

// CAS5.2: the same code in two folders is told apart by values only one has.
func TestTiedFoldersByValues(t *testing.T) {
	east, west := unit("regional/east", "aws_s3_bucket.logs"), unit("regional/west", "aws_s3_bucket.logs")
	east.Literals, west.Literals = set("acme-eu-1", "shared"), set("acme-us-2", "shared")
	units := []Unit{east, west}

	st := state("region.tfstate", "", "aws_s3_bucket.logs")
	st.Identifiers = []string{"acme-us-2-logs"}
	if r := one(t, st, units); !r.Placed || r.Unit != "regional/west" || r.Reason != ReasonValues {
		t.Fatalf("an input only west has: %+v", r)
	}
	st.Identifiers = []string{"logs-east"}
	if r := one(t, st, units); !r.Placed || r.Unit != "regional/east" || r.Reason != ReasonValues {
		t.Fatalf("the folder's own name: %+v", r)
	}
	st.Identifiers = []string{"northeast", "shared"}
	if r := one(t, st, units); r.Placed || r.Why != WhyTie || len(r.Candidates) != 2 {
		t.Fatalf("part of a word and a shared value say nothing: %+v", r)
	}
	st.Identifiers = []string{"east-west"}
	if r := one(t, st, units); r.Placed || r.Why != WhyTie {
		t.Fatalf("a value for each is still a tie: %+v", r)
	}
}

func TestModulesThatCantBeRead(t *testing.T) {
	a, b := unit("a"), unit("b")
	a.Opaque, b.Opaque = []string{""}, []string{""}
	a.Literals, b.Literals = set("name-a"), set("name-b")
	st := state("x.tfstate", "", "module.m.aws_vpc.main")
	st.Identifiers = []string{"name-b"}
	if r := one(t, st, []Unit{a, b}); !r.Placed || r.Unit != "b" || r.Reason != ReasonValues {
		t.Fatalf("values decide when no code can be read: %+v", r)
	}
	st.Identifiers = nil
	if r := one(t, st, []Unit{a, b}); r.Placed || r.Why != WhyNoMatch {
		t.Fatalf("no information: %+v", r)
	}

	// An opaque module doesn't count against the rest of the folder.
	c := unit("c", "aws_vpc.main")
	c.Opaque = []string{"module.remote."}
	if r := one(t, state("y", "z/c", "aws_vpc.main", "module.remote.aws_s3_bucket.a", "module.remote.aws_s3_bucket.b"), []Unit{c, unit("d", "aws_iam_role.r")}); !r.Placed || r.Unit != "c" || r.Matched != 1 || r.Total != 1 {
		t.Fatalf("opaque module: %+v", r)
	}
}

func TestSingleUnit(t *testing.T) {
	root := []Unit{unit("", "aws_vpc.main")}
	if r := one(t, state("t", "", "aws_vpc.main"), root); !r.Placed || r.Unit != "" || r.Reason != ReasonResources {
		t.Fatalf("resources match: %+v", r)
	}
	if r := one(t, state("t", ""), root); !r.Placed || r.Reason != ReasonOnlyUnit {
		t.Fatalf("an empty state contradicts nothing: %+v", r)
	}
	if r := one(t, state("t", "", "aws_iam_role.r"), root); r.Placed || r.Why != WhyNoMatch || !reflect.DeepEqual(r.Candidates, []string{""}) {
		t.Fatalf("other resources: %+v", r)
	}
}

func TestCollisions(t *testing.T) {
	units := []Unit{unit("network", "aws_vpc.main"), unit("vault", "vault_mount.kv")}
	older, newer := state("old", "a/network", "aws_vpc.main"), state("new", "b/network", "aws_vpc.main")
	older.Lineage, newer.Lineage, older.Serial, newer.Serial = "same", "same", 3, 9
	out := Identify([]State{older, newer}, units, nil, "")
	if out[0].Placed || out[0].Why != WhyOlderCopy || !out[1].Placed || out[1].Unit != "network" {
		t.Fatalf("the newest copy stays: %+v", out)
	}
	newer.Lineage = "other"
	out = Identify([]State{older, newer}, units, nil, "")
	if out[0].Placed || out[1].Placed || out[0].Why != WhyCompeting || out[1].Why != WhyCompeting {
		t.Fatalf("different states go to a person: %+v", out)
	}
}

func TestLineageAndNoCode(t *testing.T) {
	r := Identify([]State{state("x", "nowhere")}, []Unit{unit("network", "aws_vpc.main")}, map[string]string{"lin-x": "vault"}, "")[0]
	if !r.Placed || r.Unit != "vault" || r.Reason != ReasonLineage {
		t.Fatalf("this stack's own lineage: %+v", r)
	}
	r = Identify([]State{state("x", "infra/environments/prod/k8s/addons")}, nil, nil, "environments/prod")[0]
	if !r.Placed || r.Unit != "k8s/addons" || r.Reason != ReasonPath {
		t.Fatalf("no code: only the path: %+v", r)
	}
}

func TestDirUnit(t *testing.T) {
	for _, c := range [][3]string{
		{"environments/prod/net", "environments/prod", "net"},
		{"infra/environments/prod/net", "environments/prod", "net"},
		{"infra/environments/prod", "environments/prod", ""},
		{"environments/prod", "environments/prod", ""},
		{"other/net", "environments/prod", "other/net"},
		{"net", ".", "net"},
		{"", "environments/prod", ""},
	} {
		if got := DirUnit(c[0], c[1]); got != c[2] {
			t.Errorf("DirUnit(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// CAS5.4: only identifying attributes are read, and never a sensitive one.
func TestParseReadsOnlyIdentifiers(t *testing.T) {
	st, err := Parse("s", "d", "", []byte(`{"version":4,"serial":7,"lineage":"l","resources":[
		{"module":"module.a[\"x\"].module.b[0]","mode":"managed","type":"aws_db_instance","name":"main","instances":[
			{"attributes":{"id":"DB-1","password":"hunter2","name":"secret-name","endpoint":"db.internal"},
			 "sensitive_attributes":[[{"type":"get_attr","value":"name"}]]}]},
		{"mode":"data","type":"aws_ami","name":"u","instances":[{"attributes":{"id":"ami-1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if st.Serial != 7 || st.Resources != 1 || !reflect.DeepEqual(st.Addresses, []string{"module.a.module.b.aws_db_instance.main"}) {
		t.Fatalf("%+v", st)
	}
	if !reflect.DeepEqual(st.Identifiers, []string{"db-1"}) {
		t.Fatalf("identifiers %v", st.Identifiers)
	}
}

func fakeRepo(files map[string]string) Repo {
	var list []string
	for f := range files {
		list = append(list, f)
	}
	return Repo{Files: list, Read: func(_ context.Context, p string) ([]byte, error) {
		c, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("no %s", p)
		}
		return []byte(c), nil
	}}
}

func TestReadUnitsTerragrunt(t *testing.T) {
	repo := fakeRepo(map[string]string{
		"environments/prod/root.hcl":                         `remote_state {}`,
		"environments/prod/.firstlume/config.yaml":           "version: 1",
		"environments/prod/net/terragrunt.hcl":               "terraform {\n  source = \"../../../modules//net\"\n}\ninputs = {\n  zone = \"acme-zone-1\"\n}\n",
		"environments/prod/k8s/addons/terragrunt.hcl":        "terraform {\n  source = \"${find_in_parent_folders(\"modules\")}/addons\"\n}\n",
		"environments/prod/remote/terragrunt.hcl":            "terraform {\n  source = \"git::https://git.example.com/m.git//x?ref=v1\"\n}\n",
		"environments/prod/own/terragrunt.hcl":               "include \"root\" {}\n",
		"environments/prod/own/main.tf":                      "resource \"null_resource\" \"own\" {}\n",
		"environments/prod/net/.terragrunt-cache/x/main.tf":  "resource \"null_resource\" \"cached\" {}\n",
		"environments/prod/.terragrunt-cache/terragrunt.hcl": "",
		"environments/other/terragrunt.hcl":                  "",
		"modules/net/main.tf": "resource \"aws_vpc\" \"main\" {}\n# resource \"aws_vpc\" \"old\" {}\n" +
			"module \"sub\" {\n  source = \"./sub\"\n}\nmodule \"reg\" {\n  version = \"1\"\n  source = \"terraform-aws-modules/x/aws\"\n}\n",
		"modules/net/sub/main.tf":  "resource \"aws_subnet\" \"a\" {\n  tags = { source = \"not this\" }\n}\n",
		"modules/addons/main.tf":   "resource \"helm_release\" \"a\" {}\n",
		"modules/unrelated/x.tf":   "resource \"aws_iam_role\" \"r\" {}\n",
		"environments/prod/README": "x",
	})
	units, err := ReadUnits(context.Background(), repo, "environments/prod", true, true)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Unit{}
	var keys []string
	for _, u := range units {
		byKey[u.Key] = u
		keys = append(keys, u.Key)
	}
	if !reflect.DeepEqual(keys, []string{"k8s/addons", "net", "own", "remote"}) {
		t.Fatalf("units %v", keys)
	}
	if got := byKey["net"]; !reflect.DeepEqual(got.Addresses, set("aws_vpc.main", "module.sub.aws_subnet.a")) || !reflect.DeepEqual(got.Opaque, []string{"module.reg."}) || !got.Literals["acme-zone-1"] {
		t.Fatalf("net %+v", got)
	}
	if !byKey["k8s/addons"].Addresses["helm_release.a"] {
		t.Fatalf("addons %+v", byKey["k8s/addons"])
	}
	if got := byKey["remote"]; len(got.Addresses) != 0 || !reflect.DeepEqual(got.Opaque, []string{""}) {
		t.Fatalf("remote %+v", got)
	}
	if !byKey["own"].Addresses["null_resource.own"] {
		t.Fatalf("own %+v", byKey["own"])
	}
}

func TestReadUnitsOtherLayouts(t *testing.T) {
	// A plain Terraform stack is its one root unit.
	repo := fakeRepo(map[string]string{"infra/main.tf": "resource \"a\" \"b\" {}\n", "infra/sub/x.tf": "resource \"c\" \"d\" {}\n"})
	units, err := ReadUnits(context.Background(), repo, "infra", false, false)
	if err != nil || len(units) != 1 || units[0].Key != "" || !reflect.DeepEqual(units[0].Addresses, set("a.b")) {
		t.Fatalf("%+v %v", units, err)
	}
	// Without run --all, a unit's key is still relative to the root.hcl above it.
	repo = fakeRepo(map[string]string{"root.hcl": "", "dns/terragrunt.hcl": "", "dns/main.tf": "resource \"a\" \"b\" {}\n", "dns/sub/terragrunt.hcl": ""})
	units, err = ReadUnits(context.Background(), repo, "dns", true, false)
	if err != nil || len(units) != 1 || units[0].Key != "dns" {
		t.Fatalf("%+v %v", units, err)
	}
	// A stack's own terragrunt.hcl above its units is their shared configuration.
	repo = fakeRepo(map[string]string{"s/terragrunt.hcl": "", "s/a/terragrunt.hcl": ""})
	units, err = ReadUnits(context.Background(), repo, "s", true, true)
	if err != nil || len(units) != 1 || units[0].Key != "a" {
		t.Fatalf("%+v %v", units, err)
	}
}

func TestReadUnitsBounds(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= maxCodeFiles; i++ {
		files[fmt.Sprintf("s/f%d.tf", i)] = ""
	}
	if _, err := ReadUnits(context.Background(), fakeRepo(files), "s", false, false); !errors.Is(err, ErrTooMuchCode) {
		t.Fatalf("got %v", err)
	}
	// Modules that call each other stop at the depth limit.
	repo := fakeRepo(map[string]string{"s/main.tf": "module \"loop\" {\n  source = \"../s\"\n}\n"})
	units, err := ReadUnits(context.Background(), repo, "s", false, false)
	if err != nil || len(units[0].Opaque) != 1 || strings.Count(units[0].Opaque[0], "module.loop.") != maxModuleDepth+1 {
		t.Fatalf("%+v %v", units, err)
	}
}
