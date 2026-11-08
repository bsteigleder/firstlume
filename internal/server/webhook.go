package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/gitprovider"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

// webhookMaxBytes caps a delivery (spec 0006, R1.2).
const webhookMaxBytes = 5 << 20

// webhookEvents is what a Gitea repository's webhook has to send (R2.3).
var webhookEvents = []string{"push", "pull_request", "pull_request_sync"}

// gitlabWebhookEvents is what a GitLab project's webhook has to send (spec 0010, R5.11).
var gitlabWebhookEvents = []string{"Push events"}

// gitlabSignatureAge is how old a signed GitLab delivery may be (spec 0010, R5.2).
const gitlabSignatureAge = 5 * time.Minute

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// senderRe is a user name worth showing as is; anything else shows as the provider.
	senderRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
)

// zeroSHA is how Gitea and GitLab say a branch didn't exist before, or doesn't anymore.
func zeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// push is what Firstlume keeps of a push delivery, whatever sent it (R1.6).
type push struct {
	ref, before, after, repository, sender, delivery string
	totalCommits                                     int
}

// giteaPush holds the fields of a Gitea push that Firstlume uses (R1.6).
type giteaPush struct {
	Ref          string `json:"ref"`
	Before       string `json:"before"`
	After        string `json:"after"`
	TotalCommits int    `json:"total_commits"`
	Repository   struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// gitlabPush holds the fields of a GitLab push that Firstlume uses (spec 0010, L10).
type gitlabPush struct {
	Ref          string `json:"ref"`
	Before       string `json:"before"`
	After        string `json:"after"`
	TotalCommits int    `json:"total_commits_count"`
	UserUsername string `json:"user_username"`
	Project      struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
}

// webhookDelivery is how one provider's deliveries are checked and read.
type webhookDelivery struct {
	provider string
	// verify tells whether the delivery comes from the provider, before the body is parsed.
	verify func(secret, body []byte, h http.Header) bool
	// read returns the push to keep, or false for anything else.
	read func(body []byte, h http.Header) (push, bool)
}

var giteaDelivery = webhookDelivery{
	provider: gitprovider.Gitea,
	verify: func(secret, body []byte, h http.Header) bool {
		return validGiteaSignature(secret, body, h.Get("X-Gitea-Signature"))
	},
	read: func(body []byte, h http.Header) (push, bool) {
		var p giteaPush
		if h.Get("X-Gitea-Event") != "push" || json.Unmarshal(body, &p) != nil {
			return push{}, false
		}
		return push{ref: p.Ref, before: p.Before, after: p.After, repository: p.Repository.FullName,
			sender: p.Sender.Login, delivery: h.Get("X-Gitea-Delivery"), totalCommits: p.TotalCommits}, true
	},
}

var gitlabDelivery = webhookDelivery{
	provider: gitprovider.GitLab,
	verify: func(secret, body []byte, h http.Header) bool {
		return validGitLabDelivery(secret, body, h, time.Now())
	},
	read: func(body []byte, h http.Header) (push, bool) {
		var p gitlabPush
		if h.Get("X-Gitlab-Event") != "Push Hook" || json.Unmarshal(body, &p) != nil {
			return push{}, false
		}
		// The Idempotency-Key stays the same when GitLab delivers again; the
		// event UUID doesn't (L12).
		delivery := h.Get("Idempotency-Key")
		if delivery == "" {
			delivery = h.Get("Webhook-Id")
		}
		return push{ref: p.Ref, before: p.Before, after: p.After, repository: p.Project.PathWithNamespace,
			sender: p.UserUsername, delivery: delivery, totalCommits: p.TotalCommits}, true
	},
}

func (s *Server) giteaWebhook(w http.ResponseWriter, r *http.Request) {
	s.acceptWebhook(w, r, giteaDelivery)
}

func (s *Server) gitlabWebhook(w http.ResponseWriter, r *http.Request) {
	s.acceptWebhook(w, r, gitlabDelivery)
}

// acceptWebhook accepts a delivery. Nothing is read before it is
// authenticated, and an accepted event is only stored: the provider never
// waits for the work it leads to (R1.2, R1.3).
func (s *Server) acceptWebhook(w http.ResponseWriter, r *http.Request, d webhookDelivery) {
	unauthorized := func() { w.WriteHeader(http.StatusUnauthorized) }
	connectionID := chi.URLParam(r, "connection_id")
	if !validUUID(connectionID) {
		unauthorized()
		return
	}
	target, err := s.store.GetWebhookTarget(r.Context(), connectionID)
	if errors.Is(err, store.ErrNotFound) {
		unauthorized()
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// A connection's secret is in its own provider's format; another
	// provider's route never takes it.
	if target.Provider != d.provider {
		unauthorized()
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !d.verify(target.Secret, body, r.Header) {
		unauthorized()
		return
	}

	// Everything else is acknowledged and dropped without being stored (R1.5).
	p, ok := d.read(body, r.Header)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	event, ok := pushEvent(target, p, body)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	added, err := s.store.AddGitEvent(r.Context(), event)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if added {
		select {
		case s.gitEvents <- struct{}{}:
		default:
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func validGiteaSignature(secret, body []byte, signature string) bool {
	got, err := hex.DecodeString(signature)
	if err != nil || len(got) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// validGitLabDelivery accepts a GitLab delivery signed with the secret as the
// webhook's signing token, recent enough, or, from a GitLab without signing
// tokens, one carrying the secret as its secret token (spec 0010, R5.2, L13).
func validGitLabDelivery(secret, body []byte, h http.Header, now time.Time) bool {
	if sig := h.Get("Webhook-Signature"); sig != "" {
		key, ok := signingKey(string(secret))
		if !ok {
			return false
		}
		id, ts := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp")
		sent, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || id == "" {
			return false
		}
		if age := now.Sub(time.Unix(sent, 0)); age > gitlabSignatureAge || age < -gitlabSignatureAge {
			return false
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		want := mac.Sum(nil)
		// Several signatures may come, space separated, each "v1,<base64>".
		for _, part := range strings.Fields(sig) {
			version, value, ok := strings.Cut(part, ",")
			if !ok || version != "v1" {
				continue
			}
			if got, err := base64.StdEncoding.DecodeString(value); err == nil && hmac.Equal(got, want) {
				return true
			}
		}
		return false
	}
	token := h.Get("X-Gitlab-Token")
	return token != "" && subtle.ConstantTimeCompare([]byte(token), secret) == 1
}

// signingKey decodes a "whsec_<base64>" secret into its HMAC key.
func signingKey(secret string) ([]byte, bool) {
	raw, ok := strings.CutPrefix(secret, "whsec_")
	if !ok {
		return nil, false
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	return key, err == nil && len(key) > 0
}

// newWebhookSecret makes a secret in the provider's format. GitLab takes a
// signing token only as "whsec_" and the base64 of 32 bytes (L13).
func newWebhookSecret(provider string) (string, error) {
	if provider == gitprovider.GitLab {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return "whsec_" + base64.StdEncoding.EncodeToString(b), nil
	}
	return secrets.RandomToken("flw_")
}

// pushEvent turns a push into the event to keep. It isn't one to keep when
// it's not a push on a branch that still exists, of a repository of the
// connection's account (R1.5, R1.7, R4.6). Even though it's authenticated,
// nothing in it gets to a run unchecked: the stack's repository is what gets
// cloned.
func pushEvent(target *store.WebhookTarget, p push, body []byte) (store.NewGitEvent, bool) {
	branch, isBranch := strings.CutPrefix(p.ref, "refs/heads/")
	switch {
	case !isBranch, validateRef(branch) != nil,
		!shaRe.MatchString(p.before), !shaRe.MatchString(p.after), zeroSHA(p.after),
		!gitprovider.InAccount(target.Provider, target.Account, p.repository):
		return store.NewGitEvent{}, false
	}
	sender := p.sender
	if !senderRe.MatchString(sender) {
		sender = target.Provider
	}
	delivery := p.delivery
	if delivery == "" || len(delivery) > 100 {
		sum := sha256.Sum256(body)
		delivery = hex.EncodeToString(sum[:])
	}
	return store.NewGitEvent{
		OrganizationID: target.OrganizationID, ConnectionID: target.ConnectionID, Delivery: delivery,
		Kind: api.TriggerPush, Source: "webhook", Repository: p.repository, Ref: branch,
		BeforeSHA: p.before, AfterSHA: p.after, Sender: sender, TotalCommits: p.totalCommits,
	}, true
}

// rotateWebhookSecret generates the connection's secret and shows it this
// once, with what a webhook needs. Webhooks signing with the old one stop
// being accepted (R2.4).
func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	conn, err := s.store.GetGitConnection(r.Context(), orgID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this organization has no Git connection yet")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	secret, err := newWebhookSecret(conn.Provider)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	id, err := s.store.SetWebhookSecret(r.Context(), orgID(r), secret)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this organization has no Git connection yet")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := api.WebhookSecret{
		Provider: conn.Provider, URL: s.cfg.PublicURL + "/webhooks/v1/" + conn.Provider + "/" + id,
		Secret: secret, ContentType: "json", Events: webhookEvents,
	}
	if conn.Provider == gitprovider.GitLab {
		out.Events = gitlabWebhookEvents
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listGitEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stack := q.Get("stack")
	if stack != "" && !validUUID(stack) {
		writeError(w, http.StatusUnprocessableEntity, "stack must be a stack's id")
		return
	}
	limit := 100
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	out, err := s.store.ListGitEvents(r.Context(), orgID(r), stack, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
