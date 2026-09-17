package web

import (
	"embed"
	"html/template"
	"net/http"

	"varis/internal/burn"
	"varis/internal/retrieve"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	pool        *pgxpool.Pool
	burnMgr     *burn.Manager
	retrieveMgr *retrieve.Manager
	tmpl        *template.Template
	stagingDir  string
}

func NewServer(pool *pgxpool.Pool, burnMgr *burn.Manager, retrieveMgr *retrieve.Manager, stagingDir string) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, burnMgr: burnMgr, retrieveMgr: retrieveMgr, tmpl: tmpl, stagingDir: stagingDir}, nil
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("POST /burn", s.startBurn)
	mux.HandleFunc("GET /jobs", s.jobsFragment)
	mux.HandleFunc("POST /jobs/continue", s.continueDisc)
	mux.HandleFunc("POST /jobs/retry", s.retryDisc)

	mux.HandleFunc("GET /library", s.library)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("POST /retrieve", s.startRetrieve)
	mux.HandleFunc("POST /retrieve/read", s.readDisk)
	mux.HandleFunc("POST /retrieve/reconstruct/start", s.startReconstruction)
	mux.HandleFunc("POST /retrieve/reconstruct/read", s.readReconstructionDisc)

	mux.HandleFunc("GET /config", s.configPage)
	mux.HandleFunc("POST /config", s.addMediaType)

	mux.HandleFunc("GET /cover/{diskID}", s.cover)
}
