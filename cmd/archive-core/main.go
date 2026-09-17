package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"varis/internal/burn"
	"varis/internal/config"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/retrieve"
	"varis/internal/web"
	"varis/internal/webdav"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.SeedMediaTypes(ctx, pool); err != nil {
		log.Fatalf("seed media types: %v", err)
	}

	ex := execx.RealExecutor{}
	scratchDir := os.TempDir()

	burnMgr := burn.NewManager(burn.NewDBCataloger(pool), ex, cfg.StagingDir, cfg.SpoolDir, cfg.OpticalDevice)
	retrieveMgr := retrieve.NewManager(retrieve.NewDBCatalog(pool), ex, cfg.RetrievedDir, scratchDir, cfg.OpticalDevice)

	webServer, err := web.NewServer(pool, burnMgr, retrieveMgr, cfg.StagingDir)
	if err != nil {
		log.Fatalf("web server: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/webdav/staging/", http.StripPrefix("/webdav/staging", webdav.Handler("/", cfg.StagingDir)))
	mux.Handle("/webdav/retrieved/", http.StripPrefix("/webdav/retrieved", webdav.Handler("/", cfg.RetrievedDir)))
	webServer.Routes(mux)

	log.Printf("listening on %s", cfg.HTTPAddr)
	log.Fatal(http.ListenAndServe(cfg.HTTPAddr, mux))
}
