package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

func TestMasker(t *testing.T) {
	m := NewMasker([]string{"hunter2", "ab", "line-one\nline-two", "hunter2-extended"})
	got := m.Mask("pw=hunter2 long=hunter2-extended ab line-one line-two")
	want := "pw=*** long=*** ab *** ***"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLogShipperShipsWholeMaskedLines(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	send := func(_ context.Context, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, string(data))
		return nil
	}
	l := NewLogShipper(NewMasker([]string{"s3cret"}), nil, send)
	// A secret split across writes must still be masked.
	_, _ = l.Write([]byte("token=s3"))
	_, _ = l.Write([]byte("cret\npartial"))
	if got := l.take(false); got != "token=s3cret\n" {
		t.Fatalf("take(false) = %q", got)
	}
	l.mu.Lock()
	l.buf.Reset()
	l.mu.Unlock()
	_, _ = l.Write([]byte("token=s3cret\nend"))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(sent, "")
	if strings.Contains(all, "s3cret") {
		t.Fatalf("secret leaked: %q", all)
	}
	if all != "token=***\nend" {
		t.Fatalf("shipped %q", all)
	}
}

func TestExtractChanges(t *testing.T) {
	raw := []byte(`{"resource_changes":[
		{"address":"aws_vpc.main","mode":"managed","type":"aws_vpc","name":"main","change":{
			"actions":["update"],
			"before":{"cidr_block":"10.0.0.0/16","tags":{"env":"prod"},"id":"vpc-1"},
			"after":{"cidr_block":"10.0.0.0/16","tags":{"env":"prod","tier":"core"},"id":"vpc-1"},
			"after_unknown":{}, "before_sensitive":{}, "after_sensitive":{}}},
		{"address":"aws_subnet.a","mode":"managed","type":"aws_subnet","name":"a","change":{
			"actions":["delete","create"],
			"before":{"cidr_block":"10.0.1.0/24","id":"subnet-1","arn":"arn:1"},
			"after":{"cidr_block":"10.0.16.0/20"},
			"after_unknown":{"id":true,"arn":true},
			"replace_paths":[["cidr_block"]]}},
		{"address":"aws_db_instance.orders","mode":"managed","type":"aws_db_instance","name":"orders","change":{
			"actions":["create"],"before":null,
			"after":{"engine":"postgres","password":"hunter2","port":5432,"note":"key is s3cr3t-token"},
			"after_unknown":{"endpoint":true},
			"after_sensitive":{"password":true}}},
		{"address":"aws_db_instance.legacy","mode":"managed","type":"aws_db_instance","name":"legacy","change":{
			"actions":["delete"],"before":{"engine":"mysql"},"after":null}},
		{"address":"data.aws_ami.x","mode":"data","type":"aws_ami","name":"x","change":{"actions":["read"]}},
		{"address":"aws_iam_role.r","mode":"managed","type":"aws_iam_role","name":"r","change":{"actions":["no-op"]}}
	]}`)
	mask := NewMasker([]string{"s3cr3t-token"}).Mask
	changes, err := ExtractChanges(raw, "prod/network", mask)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 4 {
		t.Fatalf("got %d changes, want 4 (data source and no-op skipped): %+v", len(changes), changes)
	}
	byAddr := map[string]api.ResourceChange{}
	for _, c := range changes {
		if c.Unit != "prod/network" {
			t.Fatalf("unit not set: %+v", c)
		}
		byAddr[c.Address] = c
	}

	vpc := byAddr["aws_vpc.main"]
	if vpc.Action != api.ActionUpdate || len(vpc.Attributes) != 1 || vpc.Attributes[0].Path != "tags.tier" ||
		vpc.Attributes[0].Before != nil || *vpc.Attributes[0].After != "core" {
		t.Fatalf("vpc: %+v", vpc)
	}

	sub := byAddr["aws_subnet.a"]
	if sub.Action != api.ActionReplace || len(sub.Attributes) == 0 {
		t.Fatalf("subnet: %+v", sub)
	}
	first := sub.Attributes[0] // attributes forcing the replacement come first
	if first.Path != "cidr_block" || !first.ForcesReplacement || *first.Before != "10.0.1.0/24" || *first.After != "10.0.16.0/20" {
		t.Fatalf("subnet cidr: %+v", first)
	}
	for _, a := range sub.Attributes[1:] {
		if a.ForcesReplacement || *a.After != unknownValue {
			t.Fatalf("subnet attribute %+v", a)
		}
	}

	db := byAddr["aws_db_instance.orders"]
	got := map[string]string{}
	for _, a := range db.Attributes {
		if a.Before != nil {
			t.Fatalf("a create has no before: %+v", a)
		}
		got[a.Path] = *a.After
	}
	if db.Action != api.ActionCreate || got["password"] != sensitiveValue || got["port"] != "5432" ||
		got["engine"] != "postgres" || got["endpoint"] != unknownValue || got["note"] != "key is ***" {
		t.Fatalf("db attributes %v", got)
	}

	if legacy := byAddr["aws_db_instance.legacy"]; legacy.Action != api.ActionDelete || len(legacy.Attributes) != 0 {
		t.Fatalf("delete: %+v", legacy)
	}

	s := Summarize(changes)
	if s.Add != 1 || s.Change != 1 || s.Replace != 1 || s.Destroy != 1 {
		t.Fatalf("summary %+v", s)
	}
}

func TestExtractChangesLimitsAttributes(t *testing.T) {
	after := map[string]string{}
	for i := 0; i < maxAttributesPerResource+20; i++ {
		after[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", maxValueLength+50)
	}
	plan, _ := json.Marshal(map[string]any{"resource_changes": []any{map[string]any{
		"address": "x.y", "mode": "managed", "type": "x", "name": "y",
		"change": map[string]any{"actions": []string{"create"}, "after": after},
	}}})
	changes, err := ExtractChanges(plan, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := changes[0]
	if !c.Truncated || len(c.Attributes) != maxAttributesPerResource || len([]rune(*c.Attributes[0].After)) != maxValueLength+1 {
		t.Fatalf("limits not applied: truncated=%v attrs=%d", c.Truncated, len(c.Attributes))
	}
}

func TestPackUnpack(t *testing.T) {
	src := t.TempDir()
	for _, f := range []string{"prod/network/tfplan.tfplan", "prod/db/tfplan.tfplan"} {
		p := filepath.Join(src, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("plan of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := packDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := unpackDir(data, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "prod", "db", "tfplan.tfplan"))
	if err != nil || string(got) != "plan of prod/db/tfplan.tfplan" {
		t.Fatalf("round trip: %q %v", got, err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	if err := unpackDir(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("an entry escaping the directory was accepted")
	}
}

func TestFindChecksum(t *testing.T) {
	sums := []byte("abc123  terraform_1.9.8_linux_amd64.zip\nDEF456 *terraform_1.9.8_linux_arm64.zip\n")
	if got, err := findChecksum(sums, "terraform_1.9.8_linux_arm64.zip"); err != nil || got != "def456" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := findChecksum(sums, "terraform_1.9.8_darwin_arm64.zip"); err == nil {
		t.Fatal("expected error for missing asset")
	}
}

func TestToolURLs(t *testing.T) {
	tf, _ := toolFor("terraform", "1.9.8", "linux", "amd64")
	if tf.url != "https://releases.hashicorp.com/terraform/1.9.8/terraform_1.9.8_linux_amd64.zip" || tf.name != "terraform" {
		t.Fatalf("terraform: %+v", tf)
	}
	tofu, _ := toolFor("opentofu", "1.8.5", "linux", "arm64")
	if tofu.url != "https://github.com/opentofu/opentofu/releases/download/v1.8.5/tofu_1.8.5_linux_arm64.zip" || tofu.name != "tofu" {
		t.Fatalf("opentofu: %+v", tofu)
	}
	tg, _ := toolFor("terragrunt", "0.77.0", "linux", "amd64")
	if tg.url != "https://github.com/gruntwork-io/terragrunt/releases/download/v0.77.0/terragrunt_linux_amd64" || tg.zipped {
		t.Fatalf("terragrunt: %+v", tg)
	}
}

func TestResolveWorkDirRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveWorkDir(dir, "../etc"); err == nil {
		t.Fatal("expected escape to be rejected")
	}
	if got, err := resolveWorkDir(dir, ""); err != nil || got != dir {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestInheritedEnvStripsRunToken(t *testing.T) {
	t.Setenv("FIRSTLUME_RUN_TOKEN", "flr_secret")
	t.Setenv("AWS_REGION", "us-east-1")
	env := strings.Join(inheritedEnv(), "\n")
	if strings.Contains(env, "flr_secret") {
		t.Fatal("run token leaked into child environment")
	}
	if !strings.Contains(env, "AWS_REGION=us-east-1") {
		t.Fatal("cloud environment was not passed through")
	}
}

func TestGitEnvAllowsHTTPOnlyForHTTPRepositories(t *testing.T) {
	cases := map[string]string{
		"https://git.example.com/acme/infra.git": "GIT_ALLOW_PROTOCOL=https:ssh:file",
		"git@git.example.com:acme/infra.git":     "GIT_ALLOW_PROTOCOL=https:ssh:file",
		"http://127.0.0.1:3000/acme/infra.git":   "GIT_ALLOW_PROTOCOL=http:https:ssh:file",
	}
	for repo, want := range cases {
		env := gitEnv(repo, "")
		if !strings.Contains(strings.Join(env, "\n"), want) {
			t.Errorf("gitEnv(%q) = %v, want %s", repo, env, want)
		}
	}
}
