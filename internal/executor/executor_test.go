package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestInstallerRetriesLostConnections(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1: // the connection is reset before an answer, as GitHub's sometimes are
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			fmt.Fprint(w, "sums")
		}
	}))
	defer srv.Close()
	var log bytes.Buffer
	in := &Installer{Log: &log, Backoff: []time.Duration{0, 0, 0}}
	data, err := in.get(context.Background(), srv.URL)
	if err != nil || string(data) != "sums" || calls != 3 {
		t.Fatalf("got %q, %v after %d calls\n%s", data, err, calls, log.String())
	}
	if strings.Count(log.String(), "retrying") != 2 {
		t.Fatalf("log: %s", log.String())
	}
}

func TestInstallerGivesUp(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	in := &Installer{Log: io.Discard, Backoff: []time.Duration{0, 0}}
	if _, err := in.get(context.Background(), srv.URL+"/missing"); err == nil || calls != 1 {
		t.Fatalf("a 404 is not retried: %v after %d calls", err, calls)
	}
	calls = 0
	if _, err := in.get(context.Background(), srv.URL+"/down"); err == nil || calls != 3 {
		t.Fatalf("a 502 is retried as many times as there are waits: %v after %d calls", err, calls)
	}
}

func TestToolEnvProviderCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "plugin-cache")
	one := strings.Join(toolEnv("/bin/terraform", cache, false), "\n")
	if !strings.Contains(one, "TF_PLUGIN_CACHE_DIR="+cache) || strings.Contains(one, "TG_PROVIDER_CACHE") {
		t.Fatalf("one unit shares Terraform's plugin cache:\n%s", one)
	}
	all := strings.Join(toolEnv("/bin/terraform", cache, true), "\n")
	if strings.Contains(all, "TF_PLUGIN_CACHE_DIR") || !strings.Contains(all, "TG_PROVIDER_CACHE=1") ||
		!strings.Contains(all, "TG_PROVIDER_CACHE_DIR="+cache) {
		t.Fatalf("run --all uses Terragrunt's provider cache instead:\n%s", all)
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

func TestStateEnv(t *testing.T) {
	spec := &api.ExecSpec{StackID: "s1", StateToken: "fls_tok"}
	env := strings.Join(stateEnv("http://fl:8080/", spec), "\n")
	for _, want := range []string{
		"TF_HTTP_ADDRESS=http://fl:8080/state/v1/stacks/s1", "TF_HTTP_LOCK_ADDRESS=http://fl:8080/state/v1/stacks/s1",
		"TF_HTTP_USERNAME=firstlume", "TF_HTTP_PASSWORD=fls_tok", "FIRSTLUME_STATE_URL=http://fl:8080/state/v1/stacks/s1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s in\n%s", want, env)
		}
	}
	if stateEnv("http://fl", &api.ExecSpec{StackID: "s1"}) != nil {
		t.Error("no token, no state environment")
	}
}

func writeBackend(t *testing.T, dir, typ string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"terraform_version":"1.15.6"}`
	if typ != "" {
		body = fmt.Sprintf(`{"version":3,"backend":{"type":%q,"config":{}}}`, typ)
	}
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckBackends(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	if err := checkBackends(dir, "", &out); err == nil || !strings.Contains(out.String(), `backend "http" {}`) {
		t.Fatalf("no backend at all must fail with help: %v\n%s", err, out.String())
	}
	writeBackend(t, dir, "http")
	if err := checkBackends(dir, "", &out); err != nil {
		t.Fatalf("http backend: %v", err)
	}
	writeBackend(t, dir, "s3")
	if err := checkBackends(dir, "", &out); err != nil {
		t.Fatalf("the code's own backend must be accepted: %v", err)
	}

	// run --all: units are the planned ones; one with a source initializes in its cache.
	work, plans := t.TempDir(), t.TempDir()
	for _, u := range []string{"net", "app", "edge/dns"} {
		if err := os.MkdirAll(filepath.Join(plans, u), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plans, u, "tfplan.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeBackend(t, filepath.Join(work, "net"), "http")
	writeBackend(t, filepath.Join(work, "app", ".terragrunt-cache", "abc", "def", "modules", "app"), "http")
	out.Reset()
	err := checkBackends(work, plans, &out)
	if err == nil || !strings.Contains(out.String(), "(edge/dns)") {
		t.Fatalf("edge/dns has no backend: %v\n%s", err, out.String())
	}
	writeBackend(t, filepath.Join(work, "edge", "dns"), "http")
	out.Reset()
	if err := checkBackends(work, plans, &out); err != nil || !strings.Contains(out.String(), "3 unit(s)") {
		t.Fatalf("all units on http: %v\n%s", err, out.String())
	}
}

// TestGitTokenOnlyGoesToTheRepositoryHost runs git against two servers: the
// stack's Git host gets the token, any other host (a module on another
// forge, say) must not.
func TestGitTokenOnlyGoesToTheRepositoryHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var mu sync.Mutex
	auth := func(seen *string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*seen = r.Header.Get("Authorization")
			mu.Unlock()
			http.NotFound(w, r)
		}))
	}
	var atRepoHost, atOtherHost string
	repoHost, otherHost := auth(&atRepoHost), auth(&atOtherHost)
	defer repoHost.Close()
	defer otherHost.Close()

	env := append(gitEnv(repoHost.URL+"/acme/infra.git", "s3cr3t-token"), "HOME="+t.TempDir(), "PATH="+os.Getenv("PATH"))
	for _, url := range []string{repoHost.URL + "/acme/modules.git", otherHost.URL + "/someone/module.git"} {
		cmd := exec.Command("git", "ls-remote", url)
		cmd.Env = env
		_ = cmd.Run() // both answer 404; only the headers matter
	}
	mu.Lock()
	defer mu.Unlock()
	if atRepoHost == "" {
		t.Error("the repository host got no token")
	}
	if atOtherHost != "" {
		t.Errorf("another host got the token: %q", atOtherHost)
	}
}

func TestExtractChangesLabels(t *testing.T) {
	raw := []byte(`{"prior_state":{"values":{"root_module":{"resources":[
		{"type":"cloudflare_zones","values":{"name":"acme.example","result":[{"id":"z1","name":"acme.example"}]},"sensitive_values":{}}
	]}}},"resource_changes":[
		{"address":"cloudflare_dns_record.this[\"api\"]","mode":"managed","type":"cloudflare_dns_record","name":"this","change":{
			"actions":["create"],"after":{"zone_id":"z1","name":"api","type":"A","content":"203.0.113.10","proxied":true,"ttl":1,"id":null},
			"after_unknown":{"id":true},"after_sensitive":{}}},
		{"address":"cloudflare_dns_record.this[\"old\"]","mode":"managed","type":"cloudflare_dns_record","name":"this","change":{
			"actions":["delete"],"before":{"zone_id":"z1","name":"old.acme.example","type":"CNAME","content":"s3cr3t-token.acme.example"},
			"before_sensitive":{},"after":null}},
		{"address":"helm_release.app","mode":"managed","type":"helm_release","name":"app","change":{
			"actions":["update"],"after":{"name":"app","chart":"app","version":"1.2.0","namespace":"apps","values":["x"]},
			"after_sensitive":{"values":[true]},"after_unknown":{}}}
	]}`)
	changes, err := ExtractChanges(raw, "", NewMasker([]string{"s3cr3t-token"}).Mask)
	if err != nil {
		t.Fatal(err)
	}
	if l := changes[0].Label; l == nil || l.Name != "api.acme.example" || l.Record.Type != "A" || l.Record.Target != "203.0.113.10" {
		t.Errorf("create: %+v", l)
	}
	if l := changes[1].Label; l == nil || l.Name != "old.acme.example" || strings.Contains(l.Record.Target, "s3cr3t") {
		t.Errorf("delete is named by what it was, with secrets masked: %+v %+v", l, l.Record)
	}
	if l := changes[2].Label; l == nil || l.Name != "app" || len(l.Details) != 2 {
		t.Errorf("helm update: %+v", l)
	}
}
