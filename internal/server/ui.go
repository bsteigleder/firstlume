package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiHandler serves the single-page UI. Paths that are not a file fall back to
// index.html so client-side routes survive a reload. imgOrigins may also
// serve images, e.g. the sign-in provider with the users' avatars.
func uiHandler(ui fs.FS, imgOrigins ...string) http.Handler {
	files := http.FileServerFS(ui)
	csp := "default-src 'self'; img-src " + strings.Join(append([]string{"'self'", "data:"}, imgOrigins...), " ") +
		"; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		for _, prefix := range []string{"/api/", "/agent/", "/exec/"} {
			if strings.HasPrefix(r.URL.Path, prefix) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		}

		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(ui, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in every asset name.
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		if path.Ext(name) != "" {
			// A missing file (favicon.ico, a stale asset), not a client-side route.
			http.NotFound(w, r)
			return
		}

		index, err := fs.ReadFile(ui, "index.html")
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "The UI is not built into this binary. Run `make web` and rebuild.", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
