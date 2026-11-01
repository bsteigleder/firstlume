package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/s3"
	"github.com/bsteigleder/firstlume/internal/store"
)

// State storage per organization (spec 0005).

// stateStorageCheckTimeout bounds the probe of a storage (R3.1).
const stateStorageCheckTimeout = 20 * time.Second

var (
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func (s *Server) getStateStorage(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetStateStorage(r.Context(), orgID(r))
	if err != nil {
		s.storeError(w, r, err, "organization")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// putStateStorage tests the storage and only then makes it the
// organization's (R3.1).
func (s *Server) putStateStorage(w http.ResponseWriter, r *http.Request) {
	var req api.PutStateStorageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, encrypt, ok := s.stateStorageConfig(w, r, &req)
	if !ok {
		return
	}
	if cfg != nil && !s.checkStateStorage(w, r, *cfg, true) {
		return
	}
	if err := s.store.PutStateStorage(r.Context(), orgID(r), cfg, encrypt, actorName(r)); err != nil {
		var bad *store.StateStorageCheckError
		if errors.As(err, &bad) {
			writeError(w, http.StatusUnprocessableEntity, bad.Reason)
			return
		}
		s.storeError(w, r, err, "state storage")
		return
	}
	s.getStateStorage(w, r)
}

// postStateStorageCheck tests a storage without saving anything (R3.2): the
// one in the body, or the organization's current one.
func (s *Server) postStateStorageCheck(w http.ResponseWriter, r *http.Request) {
	var cfg *s3.Config
	if r.ContentLength != 0 {
		var req api.PutStateStorageRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		var ok bool
		if cfg, _, ok = s.stateStorageConfig(w, r, &req); !ok {
			return
		}
	} else {
		var err error
		if cfg, err = s.store.CurrentStateStorageConfig(r.Context(), orgID(r)); err != nil {
			var bad *store.StateStorageCheckError
			if errors.As(err, &bad) {
				writeError(w, http.StatusUnprocessableEntity, bad.Reason)
				return
			}
			s.storeError(w, r, err, "state storage")
			return
		}
	}
	if cfg != nil && !s.checkStateStorage(w, r, *cfg, false) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) checkStateStorage(w http.ResponseWriter, r *http.Request, cfg s3.Config, claim bool) bool {
	ctx, cancel := context.WithTimeout(r.Context(), stateStorageCheckTimeout)
	defer cancel()
	err := s.store.CheckStateStorage(ctx, orgID(r), cfg, claim)
	var bad *store.StateStorageCheckError
	switch {
	case err == nil:
		return true
	case errors.As(err, &bad):
		writeError(w, http.StatusUnprocessableEntity, bad.Reason)
	default:
		s.internalError(w, r, err)
	}
	return false
}

// stateStorageConfig validates a request and completes it with defaults and
// the stored secret. A nil config is the installation's storage.
func (s *Server) stateStorageConfig(w http.ResponseWriter, r *http.Request, req *api.PutStateStorageRequest) (*s3.Config, bool, bool) {
	fail := func(msg string) (*s3.Config, bool, bool) {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return nil, false, false
	}
	switch req.Kind {
	case api.StateStorageInstallation:
		if req.Encrypt != nil && !*req.Encrypt {
			return fail("the installation's storage is always encrypted")
		}
		return nil, true, true
	case api.StateStorageS3:
	default:
		return fail(`kind must be "installation" or "s3"`)
	}
	req.Endpoint = strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
	u, err := url.Parse(req.Endpoint)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return fail("endpoint must be a URL like https://s3.example.com")
	case u.Scheme == "http" && !s.cfg.StateStorageAllowHTTP:
		return fail("endpoint must be https://; this installation does not allow http:// state storages")
	case u.Scheme != "https" && u.Scheme != "http":
		return fail("endpoint must be a URL like https://s3.example.com")
	}
	req.Bucket = strings.TrimSpace(req.Bucket)
	if !bucketName.MatchString(req.Bucket) {
		return fail("bucket must be a valid S3 bucket name")
	}
	if req.Region == "" {
		req.Region = "us-east-1"
	}
	if !regionName.MatchString(req.Region) {
		return fail("region has invalid characters")
	}
	req.Folder = strings.Trim(strings.TrimSpace(req.Folder), "/")
	if req.Folder == "" {
		req.Folder = store.DefaultStateFolder
	}
	if len(req.Folder) > 256 || strings.ContainsAny(req.Folder, "\\\x00\r\n") {
		return fail("folder must be a path of up to 256 characters")
	}
	for _, seg := range strings.Split(req.Folder, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fail("folder must not have empty, . or .. parts")
		}
	}
	req.AccessKeyID = strings.TrimSpace(req.AccessKeyID)
	if req.AccessKeyID == "" {
		return fail("access_key_id is required")
	}
	encrypt := req.Encrypt == nil || *req.Encrypt
	if !encrypt && !req.PlaintextAcknowledged {
		return fail("storing state in the clear means anyone who can read the bucket reads the secrets in it: " +
			"send plaintext_acknowledged: true to confirm")
	}
	cfg, err := s.store.ResolveStateStorage(r.Context(), orgID(r), *req)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			return fail(strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
		}
		s.internalError(w, r, err)
		return nil, false, false
	}
	if _, err := s3.New(cfg); err != nil {
		return fail(err.Error())
	}
	return &cfg, encrypt, true
}

// actorName says who did something, for events.
func actorName(r *http.Request) string {
	if u := principalFrom(r.Context()).user; u != nil {
		return u.Login
	}
	return adminIdentity
}
