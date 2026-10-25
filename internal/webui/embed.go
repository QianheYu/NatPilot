// Package webui embeds the built single page application.
package webui

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	// The built SPA is embedded so a single binary serves both the API and the
	// UI. During frontend development the dist directory can be replaced by a
	// dev server (vite) talking to the API over CORS.
	"embed"
)

//go:embed all:dist
var distFS embed.FS

// entryPoint is the SPA document served for client side routes.
const entryPoint = "index.html"

// placeholder is shipped in the repository so that a fresh clone (before the
// frontend has been built) still compiles — go:embed needs at least one file —
// and serves an explanatory page instead of a 404.
const placeholder = "placeholder.html"

// HasFrontend reports whether a built SPA is embedded.
func HasFrontend() bool {
	_, err := fs.Stat(distFS, "dist/"+entryPoint)
	return err == nil
}

// sub returns the dist subtree.
func sub() (fs.FS, error) { return fs.Sub(distFS, "dist") }

// indexBytes returns the SPA entry document, falling back to the placeholder
// when the bundle has not been built into this binary.
func indexBytes(fsys fs.FS) ([]byte, error) {
	if b, err := fs.ReadFile(fsys, entryPoint); err == nil {
		return b, nil
	}
	return fs.ReadFile(fsys, placeholder)
}

// writeIndex serves the SPA document (or the build-me placeholder).
func writeIndex(w http.ResponseWriter, fsys fs.FS) error {
	doc, err := indexBytes(fsys)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, werr := w.Write(doc)
	return werr
}

// Serve writes the file at path (relative to dist) or falls back to the SPA
// entry point for client side routes.
func Serve(w http.ResponseWriter, r *http.Request, path string) error {
	fsys, err := sub()
	if err != nil {
		return err
	}
	clean := strings.TrimPrefix(path, "/")
	if clean == "" {
		clean = entryPoint
	}
	f, err := fsys.Open(clean)
	if err != nil {
		// SPA fallback: deep links such as /rules are handled by the client.
		return writeIndex(w, fsys)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return writeIndex(w, fsys)
	}
	// Long lived cache for hashed asset filenames.
	if strings.HasPrefix(clean, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", contentTypeFor(clean))
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, clean, stat.ModTime(), rs)
		return nil
	}
	data, rerr := fs.ReadFile(fsys, clean)
	if rerr != nil {
		return rerr
	}
	_, werr := w.Write(data)
	return werr
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".map"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
