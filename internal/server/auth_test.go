package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS, authorize (which logs
// in whoever claims are set), token with PKCE and userinfo. It is not Gitea,
// so the tests also show the login is plain OIDC (CA3.6).
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	down  bool
	next  map[string]any // claims of the next login
	codes map[string]fakeCode
	// groupsOnlyInUserinfo moves the groups claim from the id_token to userinfo.
	groupsOnlyInUserinfo bool
}

type fakeCode struct {
	challenge, nonce string
	claims           map[string]any
}

const fakeClientID, fakeClientSecret = "firstlume", "fake-secret"

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key, codes: map[string]fakeCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		down := f.down
		f.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		u := f.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token",
			"jwks_uri": u + "/jwks", "userinfo_endpoint": u + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE S256 required", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		code := randomSuffix()
		f.codes[code] = fakeCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), claims: f.next}
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != fakeClientID || secret != fakeClientSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		c, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || b64(sum[:]) != c.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		claims := map[string]any{
			"iss": f.srv.URL, "aud": fakeClientID, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": c.nonce,
		}
		for k, v := range c.claims {
			if k == "groups" && f.groupsOnlyInUserinfo {
				continue
			}
			claims[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-" + randomSuffix(), "token_type": "Bearer", "expires_in": 3600, "id_token": f.sign(claims),
			// The userinfo endpoint answers with the same claims.
		})
		f.mu.Lock()
		f.next = c.claims
		f.mu.Unlock()
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		claims := f.next
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(claims)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	header := b64([]byte(`{"alg":"RS256","kid":"k1","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	signing := header + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func (f *fakeIdP) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *fakeIdP) config(allowed ...string) AuthConfig {
	return AuthConfig{
		AdminTokenLogin: true,
		OIDC: &OIDCConfig{
			ProviderName: "Fake", Issuer: f.srv.URL, ClientID: fakeClientID, ClientSecret: fakeClientSecret,
			Scopes: []string{"openid", "profile", "email", "groups"}, GroupsClaim: "groups", AllowedGroups: allowed,
		},
	}
}

// person returns the claims of a new user in groups (nil = no groups claim).
func person(groups []string) map[string]any {
	sub := "sub-" + randomSuffix()
	c := map[string]any{
		"sub": sub, "preferred_username": "user-" + sub[4:], "name": "Ada Lovelace",
		"email": sub + "@example.com", "picture": "https://example.com/" + sub + ".png",
	}
	if groups != nil {
		c["groups"] = groups
	}
	return c
}

type browser struct {
	t      *testing.T
	h      *harness
	client *http.Client
}

func newBrowser(t *testing.T, h *harness) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, h: h, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) get(u string) *http.Response {
	b.t.Helper()
	resp, err := b.client.Get(u)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// login runs the whole flow and returns where Firstlume sent the browser
// at the end, plus the callback URL (to try replaying it).
func (b *browser) login(f *fakeIdP, claims map[string]any) (final string, callback string, resp *http.Response) {
	b.t.Helper()
	f.mu.Lock()
	f.next = claims
	f.mu.Unlock()
	start := b.get(b.h.url + "/auth/login?return_to=/orgs/x")
	if start.StatusCode != http.StatusFound {
		b.t.Fatalf("login: status %d", start.StatusCode)
	}
	authz := b.get(start.Header.Get("Location"))
	if authz.StatusCode != http.StatusFound {
		b.t.Fatalf("authorize: status %d", authz.StatusCode)
	}
	callback = authz.Header.Get("Location")
	resp = b.get(callback)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("callback: status %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), callback, resp
}

func (b *browser) do(method, path, origin string, body any) *http.Response {
	b.t.Helper()
	var rd *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, b.h.url+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp
}

func (b *browser) me() (api.Me, int) {
	b.t.Helper()
	resp := b.do("GET", "/api/v1/me", "", nil)
	defer resp.Body.Close()
	var me api.Me
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&me)
	}
	return me, resp.StatusCode
}

// CA3.1, CA3.3: the flow signs in, the cookie is HttpOnly and Lax, and
// logging out revokes the session on the server.
func TestLoginFlowAndLogout(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	b := newBrowser(t, h)
	claims := person([]string{"acme"})

	final, _, resp := b.login(f, claims)
	if final != "/orgs/x" {
		t.Fatalf("after login went to %q, want the return_to", final)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Fatalf("session cookie = %+v; want HttpOnly, SameSite=Lax, not Secure over http", cookie)
	}

	me, status := b.me()
	if status != http.StatusOK || me.Kind != "user" || me.User == nil {
		t.Fatalf("me: %d %+v", status, me)
	}
	if me.User.Login != claims["preferred_username"] || me.User.Name != "Ada Lovelace" || me.User.AvatarURL != claims["picture"] {
		t.Fatalf("me.user = %+v, want the provider's profile", me.User)
	}
	if len(me.User.Groups) != 1 || me.User.Groups[0] != "acme" {
		t.Fatalf("groups = %v", me.User.Groups)
	}

	// A copy of the cookie, kept before logging out, must stop working.
	stolen := &http.Cookie{Name: sessionCookie, Value: cookie.Value}
	if r := b.do("POST", "/auth/logout", h.url, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", h.url+"/api/v1/me", nil)
	req.AddCookie(stolen)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old cookie after logout: %d, want 401", r.StatusCode)
	}
}

// CA3.2: a replayed or unknown state is refused; the flow uses PKCE (the fake
// provider refuses authorize without S256 and token without the verifier).
func TestLoginStateIsSingleUse(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	b := newBrowser(t, h)
	_, callback, _ := b.login(f, person(nil))

	again := newBrowser(t, h).get(callback)
	if loc := again.Header.Get("Location"); loc != "/?login_error=state" {
		t.Fatalf("replayed callback went to %q", loc)
	}
	forged := newBrowser(t, h).get(h.url + "/auth/callback?code=x&state=forged")
	if loc := forged.Header.Get("Location"); loc != "/?login_error=state" {
		t.Fatalf("forged state went to %q", loc)
	}
}

// CA3.4 and R7.7: a session request from another origin is refused; from the
// public origin an installation administrator creates an organization and
// becomes a member.
func TestSessionOriginCheckAndFirstOrganization(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: adminsConfig(f, []string{"admins"})})
	b := newBrowser(t, h)
	b.login(f, person([]string{"admins"}))

	me, _ := b.me()
	if len(me.Organizations) != 0 {
		t.Fatalf("new user already in %v", me.Organizations)
	}
	slug := "o-" + randomSuffix()
	req := api.CreateOrganizationRequest{Slug: slug, Name: "Ada's"}
	for _, origin := range []string{"https://evil.example.com", ""} {
		r := b.do("POST", "/api/v1/orgs", origin, req)
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("POST with Origin %q: %d, want 403", origin, r.StatusCode)
		}
	}
	form, _ := http.NewRequest("POST", h.url+"/api/v1/orgs", strings.NewReader("slug="+slug))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.Header.Set("Origin", h.url)
	if r, _ := b.client.Do(form); r.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form POST: %d, want 415", r.StatusCode)
	}

	r := b.do("POST", "/api/v1/orgs", h.url, req)
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("POST from the public origin: %d", r.StatusCode)
	}
	me, _ = b.me()
	if len(me.Organizations) != 1 || me.Organizations[0].Slug != slug {
		t.Fatalf("creator's organizations = %+v", me.Organizations)
	}
}

// CA3.11: the allowed groups decide who gets in.
func TestAllowedGroups(t *testing.T) {
	f := newFakeIdP(t)
	cases := []struct {
		name    string
		allowed []string
		groups  []string // nil = no claim
		want    string
	}{
		{"member of an allowed org", []string{"acme"}, []string{"acme", "acme:infra"}, "/orgs/x"},
		{"outside the list", []string{"acme"}, []string{"other"}, "/?login_error=denied"},
		{"team required, org only", []string{"acme:infra"}, []string{"acme"}, "/?login_error=denied"},
		{"team required, in the team", []string{"acme:infra"}, []string{"acme", "acme:infra"}, "/orgs/x"},
		// Gitea leaves the claim out for someone in no organization.
		{"list set, no claim", []string{"acme"}, nil, "/?login_error=denied"},
		{"empty list, no claim", nil, nil, "/orgs/x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarnessWith(t, Config{Auth: f.config(c.allowed...)})
			claims := person(c.groups)
			final, _, _ := newBrowser(t, h).login(f, claims)
			if final != c.want {
				t.Fatalf("went to %q, want %q", final, c.want)
			}
			var n int
			if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE subject = $1`, claims["sub"]).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if signedIn := c.want == "/orgs/x"; (n == 1) != signedIn {
				t.Fatalf("users with this subject = %d, signed in = %v", n, signedIn)
			}
			if c.groups == nil && len(c.allowed) > 0 && !strings.Contains(h.logs.String(), "no groups claim") {
				t.Fatalf("log doesn't say the claim was missing:\n%s", h.logs.String())
			}
		})
	}
}

// Groups that come only from userinfo still count (some providers do that).
func TestGroupsFromUserinfo(t *testing.T) {
	f := newFakeIdP(t)
	f.groupsOnlyInUserinfo = true
	h := newHarnessWith(t, Config{Auth: f.config("acme")})
	if final, _, _ := newBrowser(t, h).login(f, person([]string{"acme"})); final != "/orgs/x" {
		t.Fatalf("went to %q", final)
	}
}

// CA3.12: with the provider down the server works with the admin token and
// the button is off; it turns on once the provider answers, without a restart.
func TestProviderDownAndBack(t *testing.T) {
	f := newFakeIdP(t)
	f.setDown(true)
	cfg := f.config()
	cfg.DiscoveryRetry = 50 * time.Millisecond
	h := newHarnessWith(t, Config{Auth: cfg})

	authConfig := func() api.AuthConfig {
		r, err := http.Get(h.url + "/auth/config")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var c api.AuthConfig
		_ = json.NewDecoder(r.Body).Decode(&c)
		return c
	}
	if c := authConfig(); c.OIDC == nil || c.OIDC.Available || c.OIDC.Name != "Fake" || !c.AdminTokenLogin {
		t.Fatalf("config while down = %+v", c)
	}
	if r := newBrowser(t, h).get(h.url + "/auth/login"); r.Header.Get("Location") != "/?login_error=unavailable" {
		t.Fatalf("login while down went to %q", r.Header.Get("Location"))
	}
	if r := newBrowser(t, h).get(h.url + "/readyz"); r.StatusCode != http.StatusOK {
		t.Fatalf("readyz while the provider is down: %d", r.StatusCode)
	}
	h.call("GET", "/api/v1/orgs", nil, http.StatusOK, nil)

	f.setDown(false)
	deadline := time.Now().Add(5 * time.Second)
	for !authConfig().OIDC.Available {
		if time.Now().After(deadline) {
			t.Fatal("the button never came back")
		}
		time.Sleep(60 * time.Millisecond)
	}
}

// R7.5: the admin token keeps working next to sessions; /me says so.
func TestAdminTokenWithLogin(t *testing.T) {
	f := newFakeIdP(t)
	h := newHarnessWith(t, Config{Auth: f.config()})
	var me api.Me
	h.call("GET", "/api/v1/me", nil, http.StatusOK, &me)
	if me.Kind != adminIdentity || me.User != nil || !me.InstallationAdmin {
		t.Fatalf("me with the admin token = %+v", me)
	}
	req, _ := http.NewRequest("GET", h.url+"/api/v1/orgs", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/orgs/acme": "/orgs/acme", "": "/", "https://evil.example.com": "/", "//evil.example.com": "/", "/\\evil": "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

// Gitea's issuer has no trailing slash; a configured one still works.
func TestIssuerTrailingSlash(t *testing.T) {
	f := newFakeIdP(t)
	cfg := f.config()
	cfg.OIDC.Issuer = f.srv.URL + "/"
	h := newHarnessWith(t, Config{Auth: cfg})
	if final, _, _ := newBrowser(t, h).login(f, person(nil)); final != "/orgs/x" {
		t.Fatalf("went to %q", final)
	}
}
