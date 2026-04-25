package server

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"

	"ue-crash-reporter/internal/storage"
)

//go:embed templates
var templateFS embed.FS

// Server holds shared dependencies for all HTTP handlers.
type Server struct {
	store   *storage.Store
	dataDir string
	tmpl    *template.Template
	log     *log.Logger
}

// New creates a Server and parses embedded HTML templates.
func New(store *storage.Store, dataDir string, logger *log.Logger) (*Server, error) {
	funcMap := template.FuncMap{
		"base": filepath.Base,
		"kb": func(n int64) string {
			if n < 1024 {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f KB", float64(n)/1024)
		},
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		store:   store,
		dataDir: dataDir,
		tmpl:    tmpl,
		log:     logger,
	}, nil
}

// Routes returns the HTTP mux for all endpoints.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// UE CrashReportClient endpoints
	mux.HandleFunc("POST /api/v1/crash", s.receiveCrash)
	mux.HandleFunc("POST /api/v2/crash", s.receiveCrash) // alias

	// Dashboard
	mux.HandleFunc("GET /", s.listCrashes)
	mux.HandleFunc("GET /crash/{id}", s.crashDetail)
	mux.HandleFunc("GET /crash/{id}/file/{filename}", s.downloadFile)

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	return loggingMiddleware(s.log, mux)
}

func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
