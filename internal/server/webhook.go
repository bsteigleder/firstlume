package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

// webhookMaxBytes caps a delivery (spec 0006, R1.2).
const webhookMaxBytes = 5 << 20

// webhookEvents is what a repository's webhook has to send (R2.3).
var webhookEvents = []string{"push", "pull_request", "pull_request_sync"}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// zeroSHA is how Gitea says a branch didn't exist before, or doesn't anymore.
func zeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// giteaPush holds the fields of a push that Firstlume uses (R1.6).
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

// giteaWebhook accepts a delivery. Nothing is read before the signature is
// checked, and an accepted event is only stored: Gitea never waits for the
// work it leads to (R1.2, R1.3).
func (s *Server) giteaWebhook(w http.ResponseWriter, r *http.Request) {
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
	if !validGiteaSignature(target.Secret, body, r.Header.Get("X-Gitea-Signature")) {
		unauthorized()
		return
	}

	// Everything else is acknowledged and dropped without being stored (R1.5).
	event, ok := pushEvent(target, r.Header.Get("X-Gitea-Event"), r.Header.Get("X-Gitea-Delivery"), body)
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

// pushEvent reads a delivery into the event to keep. It isn't one to keep when
// it's not a push on a branch that still exists, of a repository of the
// connection's account (R1.5, R1.7, R4.6). Even though it's signed, nothing in
// it gets to a run unchecked: the stack's repository is what gets cloned.
func pushEvent(target *store.WebhookTarget, kind, delivery string, body []byte) (store.NewGitEvent, bool) {
	var p giteaPush
	if kind != "push" || json.Unmarshal(body, &p) != nil {
		return store.NewGitEvent{}, false
	}
	branch, isBranch := strings.CutPrefix(p.Ref, "refs/heads/")
	owner, name, _ := strings.Cut(p.Repository.FullName, "/")
	switch {
	case !isBranch, validateRef(branch) != nil,
		!shaRe.MatchString(p.Before), !shaRe.MatchString(p.After), zeroSHA(p.After),
		!strings.EqualFold(owner, target.Account), !gitAccountRe.MatchString(name):
		return store.NewGitEvent{}, false
	}
	sender := p.Sender.Login
	if !gitAccountRe.MatchString(sender) {
		sender = "gitea"
	}
	if delivery == "" || len(delivery) > 100 {
		sum := sha256.Sum256(body)
		delivery = hex.EncodeToString(sum[:])
	}
	return store.NewGitEvent{
		OrganizationID: target.OrganizationID, ConnectionID: target.ConnectionID, Delivery: delivery,
		Kind: api.TriggerPush, Source: "webhook", Repository: p.Repository.FullName, Ref: branch,
		BeforeSHA: p.Before, AfterSHA: p.After, Sender: sender, TotalCommits: p.TotalCommits,
	}, true
}

// rotateWebhookSecret generates the connection's secret and shows it this
// once, with what a webhook needs. Webhooks signing with the old one stop
// being accepted (R2.4).
func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := secrets.RandomToken("flw_")
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
	writeJSON(w, http.StatusOK, api.WebhookSecret{
		URL: s.cfg.PublicURL + "/webhooks/v1/gitea/" + id, Secret: secret, ContentType: "json", Events: webhookEvents,
	})
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
