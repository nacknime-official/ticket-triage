package httpapi

import (
	"bytes"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func newPageHandler(t *testing.T) http.Handler {
	t.Helper()
	var logs bytes.Buffer
	return NewHandler(newFakeService(), testStreams(), alwaysReady, newTestLogger(&logs), SecurityHeaders)
}

func embedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := pageFiles.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPage(t *testing.T) {
	h := newPageHandler(t)
	tests := []struct {
		name             string
		method, path     string
		wantStatus       int
		wantType         string
		wantCacheControl string
		wantBody         []byte // nil: only checked for HEAD, which must be empty
	}{
		{"index", http.MethodGet, "/", http.StatusOK, "text/html; charset=utf-8", "no-store",
			embedded(t, "page/index.html")},
		{"script", http.MethodGet, "/assets/app.js", http.StatusOK, "text/javascript; charset=utf-8", "no-cache",
			embedded(t, "page/app.js")},
		{"stylesheet", http.MethodGet, "/assets/app.css", http.StatusOK, "text/css; charset=utf-8", "no-cache",
			embedded(t, "page/app.css")},
		{"index HEAD", http.MethodHead, "/", http.StatusOK, "text/html; charset=utf-8", "no-store", []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, newRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			hdr := rec.Header()
			if got := hdr.Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := hdr.Get("Cache-Control"); got != tt.wantCacheControl {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCacheControl)
			}
			if got := hdr.Get("Content-Security-Policy"); got != contentSecurityPolicy {
				t.Errorf("Content-Security-Policy = %q, want %q", got, contentSecurityPolicy)
			}
			if got := hdr.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q, want no-referrer: the stream token is in a URL", got)
			}
			if got := hdr.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), tt.wantBody) {
				t.Errorf("body = %d bytes, want %d", rec.Body.Len(), len(tt.wantBody))
			}
			if tt.method == http.MethodHead {
				return
			}
			if got, want := hdr.Get("Content-Length"), strconv.Itoa(len(tt.wantBody)); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}
		})
	}
}

func TestPageOtherRoutes(t *testing.T) {
	h := newPageHandler(t)
	tests := []struct {
		name         string
		method, path string
		wantStatus   int
		wantAllow    string
	}{
		{"post to the page", http.MethodPost, "/", http.StatusMethodNotAllowed, "GET, HEAD"},
		{"delete an asset", http.MethodDelete, "/assets/app.js", http.StatusMethodNotAllowed, "GET, HEAD"},
		{"index.html is not a route", http.MethodGet, "/index.html", http.StatusNotFound, ""},
		{"unknown asset", http.MethodGet, "/assets/other.js", http.StatusNotFound, ""},
		{"asset directory", http.MethodGet, "/assets/", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, newRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want the JSON error", got)
			}
		})
	}
}

// TestPageReferencesServedAssets checks that every same-origin file the HTML
// loads is served, and that the page loads nothing from elsewhere.
func TestPageReferencesServedAssets(t *testing.T) {
	h := newPageHandler(t)
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(
		string(embedded(t, "page/index.html")), -1)
	if len(refs) < 2 {
		t.Fatalf("found %d asset references in the page, want the script and the stylesheet", len(refs))
	}
	for _, m := range refs {
		ref := m[1]
		if !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
			t.Errorf("reference %q is not a same-origin path", ref)
			continue
		}
		if rec := serve(h, newRequest(http.MethodGet, ref, nil)); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", ref, rec.Code)
		}
	}
}

// TestPageKeepsTokensInMemory guards PRD §3.4 and §4.1 against a later
// edit of the page: no external requests, no inline script or style that the
// CSP would block, and no storage that could keep a token.
func TestPageKeepsTokensInMemory(t *testing.T) {
	forbidden := []string{"http://", "https://", "localStorage", "sessionStorage", "indexedDB", "document.cookie",
		"<script>", "<style", " style=", " onclick=", " onsubmit=", "innerHTML", "history.pushState",
		"history.replaceState", "location.hash"}
	err := fs.WalkDir(pageFiles, "page", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b := string(embedded(t, path))
		for _, f := range forbidden {
			if strings.Contains(b, f) {
				t.Errorf("%s contains %q", path, f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
