package s3

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEncoding(t *testing.T) {
	if got := uriEncode("a b/c~d_e.f-g+h"); got != "a%20b%2Fc~d_e.f-g%2Bh" {
		t.Errorf("uriEncode = %s", got)
	}
	if got := encodePath("/bucket/org id/x+y"); got != "/bucket/org%20id/x%2By" {
		t.Errorf("encodePath = %s", got)
	}
	q := url.Values{"prefix": {"a/b"}, "list-type": {"2"}, "continuation-token": {"x=y"}}
	if got := canonicalQuery(q); got != "continuation-token=x%3Dy&list-type=2&prefix=a%2Fb" {
		t.Errorf("canonicalQuery = %s", got)
	}
}

func TestNewValidates(t *testing.T) {
	for _, cfg := range []Config{
		{Endpoint: "ftp://x", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		{Endpoint: "http://x", AccessKeyID: "k", SecretAccessKey: "s"},
		{Endpoint: "http://x", Bucket: "b"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) accepted", cfg)
		}
	}
}

// A fake S3 answering with error documents, to check what Check reports.
func TestCheckExplainsFailures(t *testing.T) {
	cases := map[string]string{
		"NoSuchBucket":          `bucket "b" does not exist`,
		"InvalidAccessKeyId":    "access key or secret was rejected",
		"SignatureDoesNotMatch": "access key or secret was rejected",
		"AccessDenied":          "needs read and write permission",
	}
	for code, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=k/") || r.Header.Get("x-amz-date") == "" {
				t.Errorf("unsigned request: %v", r.Header)
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>` + code + `</Code><Message>nope</Message></Error>`))
		}))
		c, _ := New(Config{Endpoint: srv.URL, Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s", PathStyle: true})
		if err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", code, err, want)
		}
		srv.Close()
	}

	c, _ := New(Config{Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s", PathStyle: true})
	if err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "can't reach the endpoint") {
		t.Errorf("unreachable endpoint: %v", err)
	}
}

// testConfig is a real S3 (make dev-garage) for integration tests, e.g.
// FIRSTLUME_TEST_S3_ENDPOINT, _BUCKET, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY.
func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := Config{
		Endpoint:        os.Getenv("FIRSTLUME_TEST_S3_ENDPOINT"),
		Bucket:          os.Getenv("FIRSTLUME_TEST_S3_BUCKET"),
		Region:          os.Getenv("FIRSTLUME_TEST_S3_REGION"),
		AccessKeyID:     os.Getenv("FIRSTLUME_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("FIRSTLUME_TEST_S3_SECRET_ACCESS_KEY"),
		PathStyle:       true,
	}
	if cfg.Endpoint == "" {
		t.Skip("FIRSTLUME_TEST_S3_ENDPOINT not set (make dev-garage)")
	}
	return cfg
}

func TestRealS3(t *testing.T) {
	cfg := testConfig(t)
	cfg.Prefix = "test-" + time.Now().Format("150405.000000")
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	keys := []string{"org/stack/v1", "org/stack/v 2", "org/other/v1"}
	for _, k := range keys {
		if err := c.Put(ctx, k, []byte("data of "+k)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.Get(ctx, "org/stack/v 2")
	if err != nil || string(got) != "data of org/stack/v 2" {
		t.Fatalf("get: %q, %v", got, err)
	}
	if _, err := c.Get(ctx, "org/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	listed, err := c.List(ctx, time.Now().Add(time.Minute))
	if err != nil || len(listed) != 3 {
		t.Fatalf("list: %v, %v", listed, err)
	}
	if old, _ := c.List(ctx, time.Now().Add(-time.Hour)); len(old) != 0 {
		t.Fatalf("nothing is an hour old: %v", old)
	}
	for _, k := range keys {
		if err := c.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Delete(ctx, "org/missing"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	if listed, _ := c.List(ctx, time.Now().Add(time.Minute)); len(listed) != 0 {
		t.Fatalf("left behind: %v", listed)
	}

	// Many objects: the listing follows continuation tokens.
	for i := range 1100 {
		if err := c.Put(ctx, "many/"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Now().Format("150405.000000000"), nil); err != nil {
			t.Fatal(err)
		}
	}
	listed, err = c.List(ctx, time.Now().Add(time.Minute))
	if err != nil || len(listed) != 1100 {
		t.Fatalf("listed %d objects, %v", len(listed), err)
	}
	for _, k := range listed {
		_ = c.Delete(ctx, k)
	}
}

func TestRealS3Failures(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	check := func(cfg Config, want string) {
		t.Helper()
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Check(ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want %q", err, want)
		}
	}
	wrongSecret := cfg
	wrongSecret.SecretAccessKey = strings.Repeat("0", 64)
	check(wrongSecret, "access key or secret was rejected")
	unknownKey := cfg
	unknownKey.AccessKeyID = "GK000000000000000000000000"
	check(unknownKey, "rejected")
	noBucket := cfg
	noBucket.Bucket = "no-such-bucket-" + time.Now().Format("150405")
	check(noBucket, "does not exist")
	down := cfg
	down.Endpoint = "http://127.0.0.1:1"
	check(down, "can't reach the endpoint")
	if !bytes.Contains([]byte(cfg.Endpoint), []byte("http")) {
		t.Fatal("endpoint must be a URL")
	}
}
