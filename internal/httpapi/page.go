package httpapi

import (
	"embed"
	"net/http"
	"strconv"
)

// pageFiles is the built-in page of README §2: a submit form that shows the
// status message as it arrives over SSE. The script and stylesheet are
// separate same-origin files, so the page runs under contentSecurityPolicy
// without 'unsafe-inline'.
//
//go:embed page
var pageFiles embed.FS

// pageAsset is one embedded file of the page and the path it is served on.
type pageAsset struct {
	path        string // a ServeMux path pattern
	file        string
	contentType string
	// cacheControl is no-store for the HTML, so a page restored from the
	// history cache never brings back a ticket's tokens, and no-cache for the
	// assets, so a new build is picked up at once.
	cacheControl string
}

var pageAssets = []pageAsset{
	{"/{$}", "page/index.html", "text/html; charset=utf-8", "no-store"},
	{"/assets/app.js", "page/app.js", "text/javascript; charset=utf-8", "no-cache"},
	{"/assets/app.css", "page/app.css", "text/css; charset=utf-8", "no-cache"},
}

// handlePage registers the page routes on mux. Other methods on them get the
// JSON 405.
func handlePage(mux *http.ServeMux) {
	for _, a := range pageAssets {
		b, err := pageFiles.ReadFile(a.file)
		if err != nil {
			// The list names embedded files only, so this is a bug.
			panic("httpapi: page asset missing: " + a.file)
		}
		mux.HandleFunc("GET "+a.path, func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Type", a.contentType)
			h.Set("Content-Length", strconv.Itoa(len(b)))
			h.Set("Cache-Control", a.cacheControl)
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodHead {
				return
			}
			// A failed write means the client is gone; the access log has the status.
			_, _ = w.Write(b)
		})
		mux.Handle(a.path, methodNotAllowed(http.MethodGet, http.MethodHead))
	}
}
