package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/s3"
)

// testS3 is the real S3 the organization storage tests use (make dev-garage).
func testS3(t *testing.T) s3.Config {
	t.Helper()
	ep := os.Getenv("FIRSTLUME_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("FIRSTLUME_TEST_S3_ENDPOINT not set")
	}
	return s3.Config{
		Endpoint:        ep,
		Bucket:          os.Getenv("FIRSTLUME_TEST_S3_BUCKET"),
		Region:          os.Getenv("FIRSTLUME_TEST_S3_REGION"),
		AccessKeyID:     os.Getenv("FIRSTLUME_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("FIRSTLUME_TEST_S3_SECRET_ACCESS_KEY"),
		PathStyle:       true,
	}
}

// folderClient lists and reads a test folder directly, the way someone with
// access to the bucket would.
func folderClient(t *testing.T, cfg s3.Config, folder string) *s3.Client {
	t.Helper()
	cfg.Prefix = folder
	c, err := s3.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		keys, _ := c.List(context.Background(), time.Now().Add(time.Hour))
		for _, k := range keys {
			_ = c.Delete(context.Background(), k)
		}
	})
	return c
}

func listKeys(t *testing.T, c *s3.Client) []string {
	t.Helper()
	keys, err := c.List(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	return keys
}

func storageRequest(cfg s3.Config, folder string) api.PutStateStorageRequest {
	return api.PutStateStorageRequest{
		Kind: api.StateStorageS3, Endpoint: cfg.Endpoint, Bucket: cfg.Bucket, Region: cfg.Region, Folder: folder,
		AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey,
	}
}

// TestOrganizationStateStorage covers CASS1.1 to CASS1.6.
func TestOrganizationStateStorage(t *testing.T) {
	cfg := testS3(t)
	h := newHarnessWith(t, Config{StateStorageAllowHTTP: true})
	ctx := context.Background()
	folder := "test-org-storage/" + randomSuffix() + "/firstlume"
	bucket := folderClient(t, cfg, folder)
	var orgs []string
	t.Cleanup(func() {
		// Its storages can only be opened with this harness's keys: leave none behind for the other tests.
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = ANY($1)`, orgs)
	})
	orgs = append(orgs, h.org)

	// CASS1.1: nothing configured, the installation's storage.
	var got api.StateStorageSettings
	h.call("GET", "/state-storage", nil, http.StatusOK, &got)
	if got.Current.Kind != api.StateStorageInstallation || !got.Current.Encrypt || len(got.Previous) != 0 {
		t.Fatalf("default storage: %+v", got)
	}

	amb := h.folder("", "infra")
	prod := h.folder(amb.ID, "prod")
	st := h.stack(prod.ID, "cluster", "")
	var tok api.CreateStateTokenResponse
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusCreated, &tok)
	root, unit := h.stateURL(st.ID, ""), h.stateURL(st.ID, "zona-a/node01")
	v1 := tfState(1, "lin-root", 1, "s3cr3t-one")
	h.wantState("POST", root, tok.Token, v1, http.StatusOK)

	// CASS1.4: a storage that can't be used is refused, saying why, with no secret in it.
	bad := storageRequest(cfg, folder)
	bad.SecretAccessKey = "wrong-secret-value"
	if out := string(h.call("PUT", "/state-storage", bad, http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "rejected") ||
		strings.Contains(out, "wrong-secret-value") {
		t.Fatalf("a wrong secret: %s", out)
	}
	missing := storageRequest(cfg, folder)
	missing.Bucket = "no-such-bucket-" + randomSuffix()
	if out := string(h.call("PUT", "/state-storage", missing, http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "does not exist") {
		t.Fatalf("a missing bucket: %s", out)
	}
	plain := storageRequest(cfg, folder)
	plain.Encrypt = ptr(false)
	if out := string(h.call("PUT", "/state-storage", plain, http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "plaintext_acknowledged") {
		t.Fatalf("in the clear without confirming: %s", out)
	}
	h.call("PUT", "/state-storage", api.PutStateStorageRequest{Kind: api.StateStorageInstallation, Encrypt: ptr(false)},
		http.StatusUnprocessableEntity, nil)
	strict := newHarness(t)
	orgs = append(orgs, strict.org)
	if out := string(strict.call("PUT", "/state-storage", storageRequest(cfg, folder), http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "https://") {
		t.Fatalf("http:// without the installation allowing it: %s", out)
	}
	h.call("POST", "/state-storage/check", storageRequest(cfg, folder), http.StatusOK, nil)
	if keys := listKeys(t, bucket); len(keys) != 0 {
		t.Fatalf("a check saves nothing, not even the owner marker: %v", keys)
	}

	// Configured: the secret never comes back (CASS1.6).
	raw := string(h.call("PUT", "/state-storage", storageRequest(cfg, folder), http.StatusOK, &got))
	if strings.Contains(raw, cfg.SecretAccessKey) || !got.Current.HasSecretAccessKey || got.Current.Folder != folder ||
		got.Current.Region != cfg.Region || !got.Current.Encrypt {
		t.Fatalf("configured storage: %s", raw)
	}
	if len(got.Previous) != 1 || got.Previous[0].Kind != api.StateStorageInstallation || got.Previous[0].Versions != 1 {
		t.Fatalf("the installation keeps the earlier version: %+v", got.Previous)
	}
	if strings.Contains(h.logs.String(), cfg.SecretAccessKey) {
		t.Fatal("the secret access key is in the server log")
	}

	// The version written before is still read from where it is (R4.2).
	if got := h.wantState("GET", root, tok.Token, nil, http.StatusOK); got != string(v1) {
		t.Fatalf("read %s", got)
	}

	// CASS1.2: new versions go to the folder, under the names, encrypted.
	h.wantState("POST", root, tok.Token, tfState(2, "lin-root", 1, "s3cr3t-two"), http.StatusOK)
	h.wantState("POST", unit, tok.Token, tfState(1, "lin-unit", 1, "s3cr3t-unit"), http.StatusOK)
	named := regexp.MustCompile(`^infra/prod/cluster/(zona-a/node01/)?v00000[12]-[0-9a-f-]{36}\.tfstate\.enc$`)
	keys := listKeys(t, bucket)
	var states []string
	for _, k := range keys {
		if k == ".firstlume" {
			continue
		}
		states = append(states, k)
		if !named.MatchString(k) {
			t.Errorf("object %q is not named after the folders, stack and unit", k)
		}
		data, err := bucket.Get(ctx, k)
		if err != nil || strings.Contains(string(data), "s3cr3t") {
			t.Errorf("object %q is not encrypted (%v)", k, err)
		}
	}
	if len(states) != 2 || keys[0] != ".firstlume" {
		t.Fatalf("objects in the folder: %v", keys)
	}
	if got := h.wantState("GET", unit, tok.Token, nil, http.StatusOK); !strings.Contains(got, "s3cr3t-unit") {
		t.Fatalf("read back %s", got)
	}

	// CASS1.2b: renaming the stack sends new versions to the new path; the old one stays.
	h.call("PATCH", "/stacks/"+st.ID, api.UpdateStackRequest{Name: ptr("cluster-b")}, http.StatusOK, nil)
	h.wantState("POST", root, tok.Token, tfState(3, "lin-root", 1, "s3cr3t-three"), http.StatusOK)
	keys = listKeys(t, bucket)
	if !hasMatch(keys, `^infra/prod/cluster-b/v000003-`) || !hasMatch(keys, `^infra/prod/cluster/v000002-`) {
		t.Fatalf("after renaming: %v", keys)
	}

	// CASS1.2d: in the clear, with the confirmation; the encrypted ones still read.
	plain.PlaintextAcknowledged = true
	plain.SecretAccessKey = "" // keeps the stored one
	h.call("PUT", "/state-storage", plain, http.StatusOK, &got)
	if got.Current.Encrypt || len(got.Previous) != 1 {
		t.Fatalf("in the clear, same storage: %+v", got)
	}
	v4 := tfState(4, "lin-root", 1, "s3cr3t-four")
	h.wantState("POST", root, tok.Token, v4, http.StatusOK)
	var clear string
	for _, k := range listKeys(t, bucket) {
		if strings.HasPrefix(k, "infra/prod/cluster-b/v000004-") {
			clear = k
		}
	}
	if !strings.HasSuffix(clear, ".tfstate") {
		t.Fatalf("no plain version in %v", listKeys(t, bucket))
	}
	if data, _ := bucket.Get(ctx, clear); string(data) != string(v4) {
		t.Fatalf("the plain object is not the state Terraform wrote: %s", data)
	}
	if got := h.wantState("GET", unit, tok.Token, nil, http.StatusOK); !strings.Contains(got, "s3cr3t-unit") {
		t.Fatalf("an encrypted version reads after switching to the clear: %s", got)
	}
	h.call("GET", "/state-storage", nil, http.StatusOK, &got)
	if got.Current.Versions != 4 || got.Current.PlaintextVersions != 1 {
		t.Fatalf("counts: %+v", got.Current)
	}

	// CASS1.2c: another organization can't take the folder.
	other := newHarnessWith(t, Config{StateStorageAllowHTTP: true})
	orgs = append(orgs, other.org)
	if out := string(other.call("PUT", "/state-storage", storageRequest(cfg, folder), http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "another organization") {
		t.Fatalf("another organization's folder: %s", out)
	}

	// The cleanup removes orphans of its own and nothing else in the bucket.
	for k, v := range map[string]string{
		"notes.txt":                 "the team's notes",
		"infra/prod/cluster/README": "not ours",
		"infra/prod/cluster/v000009-orphan.tfstate.enc": "an orphan",
	} {
		if err := bucket.Put(ctx, k, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	outside := folderClient(t, cfg, strings.TrimSuffix(folder, "/firstlume"))
	if err := outside.Put(ctx, "outside.tfstate.enc", []byte("next to the folder")); err != nil {
		t.Fatal(err)
	}
	// Storages other tests left in the database may fail; this one must not.
	if _, err := h.store.PurgeState(ctx, 0); err != nil && strings.Contains(err.Error(), got.Current.ID) {
		t.Fatal(err)
	}
	keys = listKeys(t, bucket)
	if hasMatch(keys, `orphan`) || !hasMatch(keys, `^notes\.txt$`) || !hasMatch(keys, `cluster/README$`) || !hasMatch(keys, `^\.firstlume$`) {
		t.Fatalf("after the cleanup: %v", keys)
	}
	if _, err := outside.Get(ctx, "outside.tfstate.enc"); err != nil {
		t.Fatalf("the cleanup touched an object outside the folder: %v", err)
	}
	if got := h.wantState("GET", root, tok.Token, nil, http.StatusOK); got != string(v4) {
		t.Fatal("the cleanup removed a version")
	}

	// CASS1.5: the bucket is down: 503, nothing recorded, and the rest of the server works.
	if _, err := h.pool.Exec(ctx, `UPDATE state_storages SET endpoint = 'http://127.0.0.1:1', updated_at = now()
		WHERE id = (SELECT state_storage_id FROM organizations WHERE slug = $1)`, h.org); err != nil {
		t.Fatal(err)
	}
	if out := h.wantState("POST", root, tok.Token, tfState(5, "lin-root", 1, "x"), http.StatusServiceUnavailable); !strings.Contains(out, "state storage") {
		t.Fatalf("down: %s", out)
	}
	h.wantState("GET", root, tok.Token, nil, http.StatusServiceUnavailable)
	var units []api.StateUnit
	h.call("GET", "/stacks/"+st.ID+"/state", nil, http.StatusOK, &units)
	for _, u := range units {
		if u.Key == "" && u.Version != 4 {
			t.Fatalf("a failed write recorded version %d", u.Version)
		}
	}
	if out := string(h.call("POST", "/state-storage/check", nil, http.StatusUnprocessableEntity, nil)); !strings.Contains(out, "reach") {
		t.Fatalf("checking the current storage while it is down: %s", out)
	}

	// Back to the installation's storage.
	h.call("PUT", "/state-storage", api.PutStateStorageRequest{Kind: api.StateStorageInstallation}, http.StatusOK, &got)
	if got.Current.Kind != api.StateStorageInstallation || len(got.Previous) != 1 || got.Previous[0].Versions != 4 {
		t.Fatalf("back to the installation: %+v", got)
	}
	var events []map[string]any
	rows, err := h.pool.Query(ctx, `SELECT detail FROM state_events e JOIN organizations o ON o.id = e.organization_id
		WHERE o.slug = $1 AND e.kind = 'storage_changed' ORDER BY e.created_at`, h.org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var d []byte
		_ = rows.Scan(&d)
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		events = append(events, m)
	}
	rows.Close()
	if len(events) != 3 || events[1]["encrypt"] != false || events[2]["to"] != api.StateStorageInstallation {
		t.Fatalf("storage events: %v", events)
	}
}

func hasMatch(keys []string, pattern string) bool {
	re := regexp.MustCompile(pattern)
	for _, k := range keys {
		if re.MatchString(k) {
			return true
		}
	}
	return false
}
