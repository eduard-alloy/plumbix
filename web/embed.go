// Package web holds the browser interface: a Vite project in this directory
// whose build output, dist/, is embedded into the Plumbix binary.
package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built interface, as embedded at compile time. It holds
// only a .gitkeep until make web has run.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the directory is embedded by name; it cannot be missing
	}
	return sub
}

// csp keeps the app to its own origin: scripts, styles, fonts and API
// calls all come from the Plumbix server.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// Handler serves the built app from fsys: files as they are, every other
// path as index.html so the app's own router takes over. Hashed assets
// are cached for a year, the page never. Without an index.html it answers
// 503 and says how to build the app.
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" && isFile(fsys, name) {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		page, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "The Plumbix web interface is not built into this binary. Run make web and rebuild, or use the plumbix command line.")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", csp)
		_, _ = w.Write(page)
	})
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}
