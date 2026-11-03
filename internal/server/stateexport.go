package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/store"
)

var exportNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportName is the downloaded file's name, without characters that a browser
// or a shell would trip on.
func exportName(parts ...string) string {
	for i, p := range parts {
		parts[i] = strings.Trim(exportNameUnsafe.ReplaceAllString(p, "-"), "-")
	}
	return strings.Join(parts, "-")
}

// exportState downloads the current state in the clear: the whole stack as a
// tar.gz bundle that importState reads back, or one unit (?unit=, empty for the
// root) as a plain state. Everything is read before the first byte is sent, so
// a unit that can't be read fails the export instead of truncating it (R8.9).
func (s *Server) exportState(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	date := time.Now().UTC().Format("2006-01-02")

	var body bytes.Buffer
	var unit *string
	var name, contentType string
	var add func(key string, data []byte) error
	finish := func() error { return nil }
	if q := r.URL.Query(); q.Has("unit") {
		key, err := stateKey(q.Get("unit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		unit = &key
		name = exportName(st.Name, key, "state", date) + ".tfstate"
		if key == "" {
			name = exportName(st.Name, "state", date) + ".tfstate"
		}
		contentType = "application/json"
		add = func(_ string, data []byte) error {
			_, err := body.Write(data)
			return err
		}
	} else {
		name = exportName(st.Name, "state", date) + ".tar.gz"
		contentType = "application/gzip"
		gz := gzip.NewWriter(&body)
		tw := tar.NewWriter(gz)
		finish = func() error { return errors.Join(tw.Close(), gz.Close()) }
		add = func(key string, data []byte) error {
			hdr := &tar.Header{Name: path.Join(key, "terraform.tfstate"), Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			_, err := tw.Write(data)
			return err
		}
	}

	err = s.store.ExportState(r.Context(), orgID(r), st.ID, unit, actorName(r), add)
	var unreadable *store.StateExportError
	switch {
	case errors.As(err, &unreadable):
		s.log.Warn("exporting state", "path", r.URL.Path, "error", unreadable.Err)
		writeError(w, http.StatusServiceUnavailable, unreadable.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no state to export")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	if err := finish(); err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	// It holds secrets in the clear: nothing on the way may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body.Bytes())
}
