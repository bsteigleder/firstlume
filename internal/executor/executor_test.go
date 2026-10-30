package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
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

func TestSummarizePlan(t *testing.T) {
	raw := []byte(`{"resource_changes":[
		{"change":{"actions":["create"]}},
		{"change":{"actions":["update"]}},
		{"change":{"actions":["delete","create"]}},
		{"change":{"actions":["create","delete"]}},
		{"change":{"actions":["delete"]}},
		{"change":{"actions":["no-op"]}},
		{"change":{"actions":["read"]}}
	]}`)
	s, err := SummarizePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Add != 3 || s.Change != 1 || s.Destroy != 3 {
		t.Fatalf("unexpected summary %+v", s)
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
