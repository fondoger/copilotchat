package agentviz

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/index.html web/visualizer.css web/visualizer.js
var assets embed.FS

// Frontend keeps the embedded paths intact: the native window loads /web/index.html.
func Frontend() fs.FS { return assets }

func ServeAsset(w http.ResponseWriter, r *http.Request, path string) {
	name, contentType := "", ""
	switch path {
	case "/":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/visualizer.css":
		name, contentType = "visualizer.css", "text/css; charset=utf-8"
	case "/visualizer.js":
		name, contentType = "visualizer.js", "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
