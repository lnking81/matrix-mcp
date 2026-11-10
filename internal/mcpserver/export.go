package mcpserver

import (
	"bytes"
	"mime"
	"net/http"
	"strings"
)

const exportPathPrefix = "/export/"

// ExportHandler serves a rendered timeline export behind its link.
func (s *Server) ExportHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		file, ok := s.exports.Get(strings.TrimPrefix(r.URL.Path, exportPathPrefix))
		if !ok {
			setAuthResult(r, "link-invalid")
			http.NotFound(w, r)
			return
		}
		setAuthResult(r, "link")
		name := downloadFileName(file.Name, file.ContentType)
		w.Header().Set("Content-Type", file.ContentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, file.Created, bytes.NewReader(file.Data))
	})
}
