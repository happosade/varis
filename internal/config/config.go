package config

import (
	"cmp"
	"os"
)

type Config struct {
	DatabaseURL   string
	StagingDir    string
	SpoolDir      string
	RetrievedDir  string
	DryRunDir     string
	HTTPAddr      string
	OpticalDevice string
}

func Load() Config {
	return Config{
		DatabaseURL:   cmp.Or(os.Getenv("DATABASE_URL"), "postgres://varis:varis@archive-db:5432/varis?sslmode=disable"),
		StagingDir:    cmp.Or(os.Getenv("STAGING_DIR"), "/data/staging"),
		SpoolDir:      cmp.Or(os.Getenv("SPOOL_DIR"), "/data/spool"),
		RetrievedDir:  cmp.Or(os.Getenv("RETRIEVED_DIR"), "/data/retrieved"),
		DryRunDir:     cmp.Or(os.Getenv("DRYRUN_DIR"), "/data/dryrun"),
		HTTPAddr:      cmp.Or(os.Getenv("HTTP_ADDR"), ":8080"),
		OpticalDevice: cmp.Or(os.Getenv("OPTICAL_DEVICE"), "/dev/sr0"),
	}
}
