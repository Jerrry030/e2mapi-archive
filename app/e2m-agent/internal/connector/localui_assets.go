package connector

import (
	_ "embed"
	"io"
	"net/http"
	"strconv"
)

const LocalUIContentType = "text/html; charset=utf-8"

//go:embed localui/index.html
var LocalUIIndexHTML string

//go:embed localui/app.css
var localUIAppCSS string

//go:embed localui/app.js
var localUIAppJS string

// NewLocalUIHandler serves the embedded connector configuration UI without
// external assets or a separate frontend build step.
func NewLocalUIHandler(localToken ...string) http.Handler {
	token := ""
	if len(localToken) != 0 {
		token = localToken[0]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		withSecurityHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			writeLocalError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var contentType, content string
		switch r.URL.Path {
		case "/", "/index.html":
			setLocalUISessionCookie(w, r, token)
			contentType, content = LocalUIContentType, LocalUIIndexHTML
		case "/localui/app.css":
			contentType, content = "text/css; charset=utf-8", localUIAppCSS
		case "/localui/app.js":
			contentType, content = "text/javascript; charset=utf-8", localUIAppJS
		default:
			writeLocalError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", stringLength(content))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, content)
	})
}

func stringLength(value string) string {
	return strconv.Itoa(len(value))
}
