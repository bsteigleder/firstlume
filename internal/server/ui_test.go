package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUIHandler(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/style.css": {Data: []byte("body{}")},
	}
	h := uiHandler(ui)

	cases := []struct {
		method, path string
		status       int
		body         string
		cache        string
	}{
		{"GET", "/", 200, "<html>app</html>", "no-cache"},
		{"GET", "/environments/123", 200, "<html>app</html>", "no-cache"}, // client-side route
		{"GET", "/assets/app-1.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"GET", "/favicon.svg", 200, "<svg/>", ""},
		{"GET", "/../../etc/passwd", 200, "<html>app</html>", "no-cache"},
		{"GET", "/favicon.ico", 404, "not found", ""},
		{"GET", "/assets/stale-0.js", 404, "not found", ""},
		{"GET", "/api/v2/anything", 404, `"error"`, ""},
		{"GET", "/exec/v1/nope", 404, `"error"`, ""},
		{"POST", "/", 405, `"error"`, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, rec.Code, c.status)
		}
		if !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: body %q, want it to contain %q", c.method, c.path, rec.Body.String(), c.body)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s %s: Cache-Control %q, want %q", c.method, c.path, got, c.cache)
		}
		if c.status == 200 && rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: missing Content-Security-Policy", c.method, c.path)
		}
	}
}

func TestUIHandlerNotBuilt(t *testing.T) {
	rec := httptest.NewRecorder()
	uiHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("got %d %q, want 404 with a build hint", rec.Code, rec.Body.String())
	}
}
