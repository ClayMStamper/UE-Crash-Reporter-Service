package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

const pageSize = 25

// listCrashes renders the crash list dashboard.
func (s *Server) listCrashes(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize

	crashes, total, err := s.store.ListCrashes(pageSize, offset)
	if err != nil {
		s.log.Printf("list crashes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	data := map[string]any{
		"Crashes":     crashes,
		"Total":       total,
		"Page":        page,
		"TotalPages":  totalPages,
		"PrevPage":    page - 1,
		"NextPage":    page + 1,
		"HasPrev":     page > 1,
		"HasNext":     page < totalPages,
	}

	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.log.Printf("render index: %v", err)
	}
}

// crashDetail renders the detail page for a single crash.
func (s *Server) crashDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	crash, err := s.store.GetCrash(id)
	if err != nil {
		s.log.Printf("get crash %d: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if crash == nil {
		http.NotFound(w, r)
		return
	}

	if err := s.tmpl.ExecuteTemplate(w, "detail.html", crash); err != nil {
		s.log.Printf("render detail: %v", err)
	}
}

// downloadFile serves a raw file from a crash report.
func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	crash, err := s.store.GetCrash(id)
	if err != nil || crash == nil {
		http.NotFound(w, r)
		return
	}

	want := r.PathValue("filename")
	for _, f := range crash.Files {
		if f.Filename == want {
			abs := filepath.Join(s.dataDir, f.StorePath)
			// Prevent path traversal.
			if !isSubPath(s.dataDir, abs) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Disposition",
				fmt.Sprintf(`attachment; filename="%s"`, f.Filename))
			w.Header().Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
			w.WriteHeader(http.StatusOK)
			w.Write(data) //nolint:errcheck
			return
		}
	}
	http.NotFound(w, r)
}

// isSubPath returns true if child is inside parent (after cleaning both).
func isSubPath(parent, child string) bool {
	parent = filepath.Clean(parent) + string(filepath.Separator)
	child = filepath.Clean(child)
	return len(child) >= len(parent) && child[:len(parent)] == parent
}
