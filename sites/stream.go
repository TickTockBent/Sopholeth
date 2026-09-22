// Package sites embeds the same assets deployed by the standalone static site.
package sites

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed soph.stream/index.html soph.stream/styles.css soph.stream/viewer.js
var streamAssets embed.FS

func StreamHandler() http.Handler {
	root, err := fs.Sub(streamAssets, "soph.stream")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/", "/index.html", "/styles.css", "/viewer.js":
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
