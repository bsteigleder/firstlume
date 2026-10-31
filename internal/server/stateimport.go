package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

// State tokens and imports (spec 0002, R7 and R9).

func (s *Server) listStateTokens(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListStateTokens(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createStateToken(w http.ResponseWriter, r *http.Request) {
	var req api.CreateStateTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateName("name", req.Name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusUnprocessableEntity, "expires_at must be in the future")
		return
	}
	out, err := s.store.CreateStateToken(r.Context(), orgID(r), id(r), req, adminIdentity)
	if errors.Is(err, store.ErrDuplicate) {
		writeError(w, http.StatusConflict, "the stack already has a state token with this name")
		return
	}
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) revokeStateToken(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !validUUID(token) {
		writeError(w, http.StatusNotFound, "state token not found")
		return
	}
	if err := s.store.RevokeStateToken(r.Context(), orgID(r), id(r), token, adminIdentity); err != nil {
		s.storeError(w, r, err, "state token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// importMaxFiles bounds the entries of a bundle.
const importMaxFiles = 10000

// importState imports a tar.gz bundle, or a single state, into the stack's
// units. The request is multipart: "file", and optionally "prefix" (removed
// from the bundle's paths), "unit" (the key for a single state) and one
// "replace" per existing unit to overwrite. With ?dry_run=true nothing is
// written. Any problem with the upload rejects all of it (R9.5).
func (s *Server) importState(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry_run") == "true"
	form, err := readImportForm(w, r, 4*s.cfg.StateMaxBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res := api.StateImportResult{DryRun: dryRun, Units: []api.StateImportUnit{}}
	var items []store.StateImport
	items, res.Ignored, res.Problems = readImport(form.file, form.prefix, form.unit, s.cfg.StateMaxBytes, 4*s.cfg.StateMaxBytes)
	replace := map[string]bool{}
	for _, raw := range form.replace {
		key, err := stateKey(raw)
		if err != nil {
			res.Problems = append(res.Problems, api.StateImportProblem{Path: raw, Error: err.Error()})
			continue
		}
		if !hasImport(items, key) {
			res.Problems = append(res.Problems, api.StateImportProblem{Path: raw, Error: "marked to replace, but the upload has no state for this unit"})
		}
		replace[key] = true
	}
	if len(items) == 0 && len(res.Problems) == 0 {
		res.Problems = append(res.Problems, api.StateImportProblem{Path: "file", Error: "no terraform.tfstate found"})
	}
	if len(res.Problems) > 0 {
		res.Error = "nothing was imported: fix the problems listed"
		writeJSON(w, http.StatusUnprocessableEntity, res)
		return
	}
	units, err := s.store.ImportState(r.Context(), orgID(r), id(r), items, replace, dryRun, adminIdentity)
	var conflict *store.StateConflictError
	switch {
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, conflict.Reason)
		return
	case err != nil:
		s.storeError(w, r, err, "stack")
		return
	}
	res.Units = units
	writeJSON(w, http.StatusOK, res)
}

func hasImport(items []store.StateImport, key string) bool {
	for _, it := range items {
		if it.Key == key {
			return true
		}
	}
	return false
}

type importForm struct {
	file         []byte
	prefix, unit string
	replace      []string
	sawFile      bool
}

// readImportForm reads the multipart request in memory: the server's root
// filesystem is read-only, so nothing may spill to temporary files.
func readImportForm(w http.ResponseWriter, r *http.Request, maxBytes int64) (*importForm, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		return nil, errors.New("send a multipart/form-data request with the bundle in the field \"file\"")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	var f importForm
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the upload: %w", err)
		}
		limit := int64(4 << 10)
		if part.FormName() == "file" {
			limit = maxBytes
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			return nil, fmt.Errorf("reading the upload: %w", err)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("the field %q is larger than the limit of %d bytes", part.FormName(), limit)
		}
		switch part.FormName() {
		case "file":
			f.file, f.sawFile = data, true
		case "prefix":
			f.prefix = string(data)
		case "unit":
			f.unit = string(data)
		case "replace":
			f.replace = append(f.replace, string(data))
		}
	}
	if !f.sawFile {
		return nil, errors.New("the field \"file\" is missing")
	}
	return &f, nil
}

// readImport turns an upload into the states to import. A tar.gz bundle maps
// each <path>/terraform.tfstate to the unit <path>, after removing prefix
// (R9.2), also when the state sits in the unit's .terragrunt-cache; anything
// else is a single state for unit. Backups, macOS metadata
// and Terraform's own working directories are ignored; every other file,
// link, path leaving the bundle, oversized or invalid state is a problem.
func readImport(data []byte, prefix, unit string, maxState, maxTotal int64) (items []store.StateImport, ignored []string, problems []api.StateImportProblem) {
	problem := func(p, format string, args ...any) {
		problems = append(problems, api.StateImportProblem{Path: p, Error: fmt.Sprintf(format, args...)})
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		key, err := stateKey(unit)
		switch {
		case err != nil:
			problem("unit", "%v", err)
		case int64(len(data)) > maxState:
			problem("file", "state larger than the limit of %d bytes", maxState)
		default:
			if err := store.CheckState(data); err != nil {
				problem("file", "%v", err)
			} else {
				items = append(items, store.StateImport{Key: key, Data: data})
			}
		}
		return items, nil, problems
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		problem("file", "not a tar.gz bundle nor a Terraform state")
		return nil, nil, problems
	}
	prefix = strings.Trim(path.Clean("/"+strings.TrimSpace(prefix)), "/")
	tr := tar.NewReader(io.LimitReader(gz, maxTotal))
	seen := map[string]string{}
	for files := 0; ; files++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problem("file", "the bundle is damaged or larger than %d bytes uncompressed: %v", maxTotal, err)
			break
		}
		if files >= importMaxFiles {
			problem("file", "the bundle has more than %d entries", importMaxFiles)
			break
		}
		name := hdr.Name
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		case tar.TypeReg:
		case tar.TypeSymlink, tar.TypeLink:
			problem(name, "links are not allowed")
			continue
		default:
			problem(name, "only regular files are allowed")
			continue
		}
		clean := path.Clean(name)
		if path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
			problem(name, "the path leaves the bundle")
			continue
		}
		base := path.Base(clean)
		switch {
		case strings.HasPrefix(base, "._"), strings.HasSuffix(base, ".tfstate.backup"),
			strings.Contains("/"+clean+"/", "/.terraform/"):
			ignored = append(ignored, name)
			continue
		case base != "terraform.tfstate":
			problem(name, "not a state: only <unit path>/terraform.tfstate files are imported")
			continue
		}
		dir := path.Dir(clean)
		// Terragrunt runs a unit in <unit>/.terragrunt-cache/<hash>/<hash>,
		// where a local state ends up.
		if unitDir, _, ok := strings.Cut("/"+dir+"/", "/.terragrunt-cache/"); ok {
			dir = strings.TrimPrefix(unitDir, "/")
			if dir == "" {
				dir = "."
			}
		}
		if prefix != "" {
			switch {
			case dir == prefix:
				dir = "."
			case strings.HasPrefix(dir, prefix+"/"):
				dir = strings.TrimPrefix(dir, prefix+"/")
			default:
				problem(name, "outside the prefix %q", prefix)
				continue
			}
		}
		key, err := stateKey(dir)
		if err != nil {
			problem(name, "%v", err)
			continue
		}
		if hdr.Size > maxState {
			problem(name, "state larger than the limit of %d bytes", maxState)
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			problem(name, "reading: %v", err)
			continue
		}
		if err := store.CheckState(content); err != nil {
			problem(name, "%v", err)
			continue
		}
		if other, ok := seen[key]; ok {
			problem(name, "the same unit as %s", other)
			continue
		}
		seen[key] = name
		items = append(items, store.StateImport{Key: key, Data: content})
	}
	return items, ignored, problems
}
