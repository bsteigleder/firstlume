package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

// adminsConfig is the fake provider with an admin group.
func adminsConfig(f *fakeIdP, admins []string, allowed ...string) AuthConfig {
	cfg := f.config(allowed...)
	cfg.OIDC.AdminGroups = admins
	return cfg
}

func (b *browser) status(method, path string, body any) int {
	b.t.Helper()
	r := b.do(method, path, b.h.url, body)
	r.Body.Close()
	return r.StatusCode
}

func (b *browser) orgSlugs() []string {
	b.t.Helper()
	r := b.do("GET", "/api/v1/orgs", "", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b.t.Fatalf("GET /orgs: %d", r.StatusCode)
	}
	var orgs []api.Organization
	if err := json.NewDecoder(r.Body).Decode(&orgs); err != nil {
		b.t.Fatal(err)
	}
	out := make([]string, len(orgs))
	for i, o := range orgs {
		out[i] = o.Slug
	}
	return out
}

func newOrg(h *harness) string {
	h.t.Helper()
	slug := "o-" + randomSuffix()
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: slug, Name: slug}, http.StatusCreated, nil)
	return slug
}

func (h *harness) invite(org, login string) api.InviteResult {
	h.t.Helper()
	var out api.InviteResult
	h.call("POST", "/api/v1/orgs/"+org+"/members", api.InviteRequest{Login: login}, http.StatusCreated, &out)
	return out
}

// CA3.14: a group in adminGroups makes an installation administrator.
func TestInstallationAdmin(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: adminsConfig(f, []string{"acme:infra"})})
	create := func() api.CreateOrganizationRequest {
		return api.CreateOrganizationRequest{Slug: "o-" + randomSuffix(), Name: "New"}
	}

	admin := newBrowser(t, h)
	claims := person([]string{"acme", "acme:infra"})
	admin.login(f, claims)
	if me, _ := admin.me(); !me.InstallationAdmin {
		t.Fatalf("me = %+v, want an installation admin", me)
	}
	if !slices.Contains(admin.orgSlugs(), h.org) {
		t.Fatal("an installation admin doesn't see every organization")
	}
	if s := admin.status("GET", "/api/v1/orgs/"+h.org+"/tree", nil); s != http.StatusOK {
		t.Fatalf("admin opening an organization they aren't in: %d", s)
	}
	req := create()
	if s := admin.status("POST", "/api/v1/orgs", req); s != http.StatusCreated {
		t.Fatalf("admin creating an organization: %d", s)
	}
	if me, _ := admin.me(); len(me.Organizations) != 1 || me.Organizations[0].Slug != req.Slug {
		t.Fatalf("the creator should be a member: %+v", me.Organizations)
	}

	other := newBrowser(t, h)
	other.login(f, person([]string{"acme"}))
	if me, _ := other.me(); me.InstallationAdmin {
		t.Fatal("someone outside the admin group is an installation admin")
	}
	if s := other.status("POST", "/api/v1/orgs", create()); s != http.StatusForbidden {
		t.Fatalf("non-admin creating an organization: %d, want 403", s)
	}

	// Leaving the group takes effect on the next login.
	claims["groups"] = []string{"acme"}
	admin.login(f, claims)
	if me, _ := admin.me(); me.InstallationAdmin {
		t.Fatal("still an installation admin after leaving the group")
	}
	if slices.Contains(admin.orgSlugs(), h.org) {
		t.Fatal("a former admin still sees every organization")
	}
}

// CA3.14: an admin group gets in even when allowedGroups doesn't name it.
func TestAdminGroupGetsPastAllowedGroups(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: adminsConfig(f, []string{"platform:admins"}, "acme")})
	b := newBrowser(t, h)
	if final, _, _ := b.login(f, person([]string{"platform:admins"})); final != "/orgs/x" {
		t.Fatalf("admin outside allowedGroups went to %q", final)
	}
	if final, _, _ := newBrowser(t, h).login(f, person([]string{"platform"})); final != "/?login_error=denied" {
		t.Fatalf("someone in neither list went to %q", final)
	}
}

// CA3.14: with no admin groups only the admin token creates organizations.
func TestNoAdminGroups(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	b := newBrowser(t, h)
	b.login(f, person([]string{"acme"}))
	if s := b.status("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: "o-" + randomSuffix(), Name: "N"}); s != http.StatusForbidden {
		t.Fatalf("user creating an organization: %d, want 403", s)
	}
	newOrg(h)
}

// CA3.15: a user sees only their organizations, and every route of another one
// answers exactly like an address that doesn't exist.
func TestOrganizationVisibility(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	mine, theirs := newOrg(h), h.org
	st := h.stack("", "s", "")
	run := h.run(st.ID, api.RunPlan)

	claims := person([]string{"acme"})
	b := newBrowser(t, h)
	b.login(f, claims)
	if orgs := b.orgSlugs(); len(orgs) != 0 {
		t.Fatalf("before any invitation the user sees %v", orgs)
	}
	h.invite(mine, claims["preferred_username"].(string))

	if orgs := b.orgSlugs(); len(orgs) != 1 || orgs[0] != mine {
		t.Fatalf("GET /orgs = %v, want only %s", orgs, mine)
	}
	if me, _ := b.me(); len(me.Organizations) != 1 || me.Organizations[0].Slug != mine {
		t.Fatalf("/me organizations = %+v", me.Organizations)
	}
	if s := b.status("GET", "/api/v1/orgs/"+mine+"/tree", nil); s != http.StatusOK {
		t.Fatalf("own organization: %d", s)
	}

	missing := b.do("GET", "/api/v1/orgs/does-not-exist", "", nil)
	defer missing.Body.Close()
	var wantBody map[string]any
	_ = json.NewDecoder(missing.Body).Decode(&wantBody)

	p := "/api/v1/orgs/" + theirs
	for _, route := range []struct{ method, path string }{
		{"GET", ""}, {"PATCH", ""}, {"DELETE", ""}, {"GET", "/tree"}, {"GET", "/settings"}, {"PUT", "/settings"},
		{"POST", "/folders"}, {"POST", "/stacks"}, {"GET", "/stacks/" + st.ID}, {"GET", "/stacks/" + st.ID + "/runs"},
		{"POST", "/stacks/" + st.ID + "/runs"}, {"GET", "/stacks/" + st.ID + "/state"}, {"GET", "/stacks/" + st.ID + "/resources"},
		{"GET", "/runs?active=true"}, {"GET", "/runs/" + run.ID}, {"GET", "/runs/" + run.ID + "/logs"}, {"POST", "/runs/" + run.ID + "/approve"},
		{"GET", "/git/connection"}, {"GET", "/vault"}, {"GET", "/state-storage"}, {"GET", "/agents"}, {"POST", "/agents"},
		{"GET", "/members"}, {"POST", "/members"},
	} {
		var body any
		if route.method != "GET" && route.method != "DELETE" {
			body = map[string]any{}
		}
		r := b.do(route.method, p+route.path, h.url, body)
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", route.method, route.path, r.StatusCode)
		}
		if got["error"] != wantBody["error"] {
			t.Errorf("%s %s: body %v differs from a missing organization's %v", route.method, route.path, got, wantBody)
		}
	}

	// The admin token keeps seeing and changing both.
	h.call("GET", p+"/tree", nil, http.StatusOK, nil)
	h.call("GET", "/api/v1/orgs/"+mine+"/tree", nil, http.StatusOK, nil)
	h.call("PATCH", p, api.UpdateOrganizationRequest{Name: "Renamed"}, http.StatusOK, nil)
}

// CA3.16: inviting someone known gives access now; anyone else waits for their
// first sign-in, however they capitalize the login.
func TestInvitations(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	org := newOrg(h)
	members := func() api.MemberList {
		var out api.MemberList
		h.call("GET", "/api/v1/orgs/"+org+"/members", nil, http.StatusOK, &out)
		return out
	}

	known := person(nil)
	knownB := newBrowser(t, h)
	knownB.login(f, known)
	knownLogin := known["preferred_username"].(string)
	if r := h.invite(org, knownLogin); r.Member == nil || r.Invite != nil {
		t.Fatalf("inviting a known user: %+v", r)
	}
	if orgs := knownB.orgSlugs(); len(orgs) != 1 || orgs[0] != org {
		t.Fatalf("known user's organizations right after the invitation: %v", orgs)
	}
	// Twice is a clear refusal, not a duplicate.
	raw := h.call("POST", "/api/v1/orgs/"+org+"/members", api.InviteRequest{Login: knownLogin}, http.StatusConflict, nil)
	if !strings.Contains(string(raw), "already a member") {
		t.Fatalf("second invitation answered %s", raw)
	}
	if m := members(); len(m.Members) != 1 || m.Members[0].Login != knownLogin {
		t.Fatalf("members = %+v", m.Members)
	}

	// Never signed in: pending, listed, and renewed rather than doubled.
	pending := person(nil)
	login := pending["preferred_username"].(string)
	if r := h.invite(org, strings.ToUpper(login)); r.Invite == nil || r.Member != nil {
		t.Fatalf("inviting someone new: %+v", r)
	}
	h.invite(org, strings.ToUpper(login))
	if m := members(); len(m.Invites) != 1 || m.Invites[0].InvitedBy != adminIdentity {
		t.Fatalf("invites = %+v", m.Invites)
	}
	b := newBrowser(t, h)
	b.login(f, pending)
	if orgs := b.orgSlugs(); len(orgs) != 1 || orgs[0] != org {
		t.Fatalf("after the first sign-in the invited user sees %v", orgs)
	}
	if m := members(); len(m.Invites) != 0 || len(m.Members) != 2 {
		t.Fatalf("after accepting: %+v", m)
	}

	// A cancelled invitation gives nothing.
	cancelled := person(nil)
	inv := h.invite(org, cancelled["preferred_username"].(string)).Invite
	h.call("DELETE", "/api/v1/orgs/"+org+"/invites/"+inv.ID, nil, http.StatusNoContent, nil)
	h.call("DELETE", "/api/v1/orgs/"+org+"/invites/"+inv.ID, nil, http.StatusNotFound, nil)
	b = newBrowser(t, h)
	b.login(f, cancelled)
	if orgs := b.orgSlugs(); len(orgs) != 0 {
		t.Fatalf("a cancelled invitation gave access to %v", orgs)
	}

	// Neither does one over 30 days old, and it leaves the list.
	expired := person(nil)
	inv = h.invite(org, expired["preferred_username"].(string)).Invite
	if inv.ExpiresAt.Sub(inv.CreatedAt).Round(time.Hour) != inviteTTL {
		t.Fatalf("invitation lasts %s", inv.ExpiresAt.Sub(inv.CreatedAt))
	}
	if _, err := h.pool.Exec(context.Background(), `UPDATE organization_invites SET expires_at = now() - interval '1 second' WHERE id = $1`, inv.ID); err != nil {
		t.Fatal(err)
	}
	if m := members(); len(m.Invites) != 0 {
		t.Fatalf("an expired invitation is still listed: %+v", m.Invites)
	}
	b = newBrowser(t, h)
	b.login(f, expired)
	if orgs := b.orgSlugs(); len(orgs) != 0 {
		t.Fatalf("an expired invitation gave access to %v", orgs)
	}

	// A member invites, and it records who did; someone outside can't.
	if s := knownB.status("POST", "/api/v1/orgs/"+org+"/members", api.InviteRequest{Login: "new-" + randomSuffix()}); s != http.StatusCreated {
		t.Fatalf("a member inviting: %d", s)
	}
	if m := members(); len(m.Invites) != 1 || m.Invites[0].InvitedBy != knownLogin {
		t.Fatalf("invites = %+v", m.Invites)
	}
	if s := b.status("POST", "/api/v1/orgs/"+org+"/members", api.InviteRequest{Login: "x"}); s != http.StatusNotFound {
		t.Fatalf("someone outside inviting: %d, want 404", s)
	}
	for _, bad := range []string{"", "  ", "two words"} {
		h.call("POST", "/api/v1/orgs/"+org+"/members", api.InviteRequest{Login: bad}, http.StatusUnprocessableEntity, nil)
	}
}

// CA3.17: removal ends access at the next request, with the session still open,
// and leaves a user with no organization with none to see.
func TestRemoveMember(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	org := newOrg(h)
	claims := person(nil)
	b := newBrowser(t, h)
	b.login(f, claims)
	m := h.invite(org, claims["preferred_username"].(string)).Member
	if s := b.status("GET", "/api/v1/orgs/"+org+"/tree", nil); s != http.StatusOK {
		t.Fatalf("before removal: %d", s)
	}
	h.call("DELETE", "/api/v1/orgs/"+org+"/members/"+m.UserID, nil, http.StatusNoContent, nil)
	h.call("DELETE", "/api/v1/orgs/"+org+"/members/"+m.UserID, nil, http.StatusNotFound, nil)
	if s := b.status("GET", "/api/v1/orgs/"+org+"/tree", nil); s != http.StatusNotFound {
		t.Fatalf("after removal: %d, want 404", s)
	}
	if me, status := b.me(); status != http.StatusOK || len(me.Organizations) != 0 {
		t.Fatalf("after removal /me: %d %+v", status, me)
	}
	if orgs := b.orgSlugs(); len(orgs) != 0 {
		t.Fatalf("after removal the user sees %v", orgs)
	}

	// Someone can leave on their own.
	h.invite(org, claims["preferred_username"].(string))
	if s := b.status("DELETE", "/api/v1/orgs/"+org+"/members/"+m.UserID, nil); s != http.StatusNoContent {
		t.Fatalf("leaving: %d", s)
	}
}

// CA3.7 and CA3.10: runs and approvals record the user's login, and the user's
// own runs come first in the queue.
func TestActorAndMyRunsFirst(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	claims := person(nil)
	login := claims["preferred_username"].(string)
	email := claims["email"].(string)
	b := newBrowser(t, h)
	b.login(f, claims)
	h.invite(h.org, login)

	theirs := h.stack("", "theirs", "")
	byCommit := h.stack("", "by-commit", "")
	byLogin := h.stack("", "by-login", "")
	other := h.run(theirs.ID, api.RunPlan)
	mineCommit := h.run(byCommit.ID, api.RunPlan)
	if other.CreatedBy != adminIdentity {
		t.Fatalf("admin token's run created by %q", other.CreatedBy)
	}
	if _, err := h.pool.Exec(context.Background(), `UPDATE runs SET commit_author_email = $2 WHERE id = $1`, mineCommit.ID, strings.ToUpper(email)); err != nil {
		t.Fatal(err)
	}

	r := b.do("POST", "/api/v1/orgs/"+h.org+"/stacks/"+byLogin.ID+"/runs", h.url, api.CreateRunRequest{Type: api.RunPlan})
	defer r.Body.Close()
	var mine api.Run
	if err := json.NewDecoder(r.Body).Decode(&mine); err != nil || r.StatusCode != http.StatusCreated {
		t.Fatalf("user's run: %d %v", r.StatusCode, err)
	}
	if mine.CreatedBy != login {
		t.Fatalf("user's run created by %q, want %q", mine.CreatedBy, login)
	}

	// The newest of the others would come first if the user's didn't.
	newest := h.stack("", "newest", "")
	latest := h.run(newest.ID, api.RunPlan)
	q := b.do("GET", "/api/v1/orgs/"+h.org+"/runs?active=true", "", nil)
	defer q.Body.Close()
	var queue []api.ActiveRun
	if err := json.NewDecoder(q.Body).Decode(&queue); err != nil {
		t.Fatal(err)
	}
	if len(queue) != 4 || !slices.Contains([]string{mine.ID, mineCommit.ID}, queue[0].ID) || !slices.Contains([]string{mine.ID, mineCommit.ID}, queue[1].ID) {
		t.Fatalf("queue = %+v; the user's runs should come first", queue)
	}
	if queue[2].ID != latest.ID || queue[3].ID != other.ID {
		t.Fatalf("the others' runs are out of order: %+v", queue)
	}

	// Approving records who did.
	if _, err := h.pool.Exec(context.Background(), `UPDATE runs SET status = 'awaiting_approval', has_changes = true WHERE id = $1`, mine.ID); err != nil {
		t.Fatal(err)
	}
	a := b.do("POST", "/api/v1/orgs/"+h.org+"/runs/"+mine.ID+"/approve", h.url, map[string]any{})
	defer a.Body.Close()
	var approved api.Run
	if err := json.NewDecoder(a.Body).Decode(&approved); err != nil || a.StatusCode != http.StatusOK {
		t.Fatalf("approving: %d %v", a.StatusCode, err)
	}
	if approved.ApprovedBy != login {
		t.Fatalf("approved by %q, want %q", approved.ApprovedBy, login)
	}
}
