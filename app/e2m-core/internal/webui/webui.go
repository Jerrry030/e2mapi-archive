// Package webui serves the embedded E2M console single-page app. The built
// assets live in dist/ and are embedded at compile time; when dist is empty
// (a bare checkout before `npm run build`), Handler serves a small placeholder
// so the binary still builds and runs.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the SPA. Unknown non-API paths
// fall back to index.html so client-side routing works. Returns (handler, true)
// when a real build is embedded, or (placeholder, false) otherwise.
func Handler() (http.Handler, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return placeholder(), false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return placeholder(), false
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the asset if it exists; otherwise serve index.html for SPA routes.
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}), true
}

func placeholder() http.Handler {
	const page = `<!doctype html><html><head><meta charset="utf-8"><title>E2M Console</title></head>
<body style="font-family:system-ui;margin:40px;color:#333">
<h1>E2M Console</h1>
<p>The web console has not been built into this binary.</p>
<p>Run <code>npm run build</code> in <code>web/console</code> and rebuild, or use the Docker image which builds it automatically.</p>
<p>The API is available under <code>/api/v1</code>.</p>
</body></html>`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}
