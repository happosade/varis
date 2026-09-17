package config

import "testing"

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("STAGING_DIR", "")
	t.Setenv("SPOOL_DIR", "")
	t.Setenv("RETRIEVED_DIR", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("OPTICAL_DEVICE", "")

	cfg := Load()

	if cfg.StagingDir != "/data/staging" {
		t.Errorf("StagingDir = %q, want /data/staging", cfg.StagingDir)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")

	cfg := Load()

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want :9090", cfg.HTTPAddr)
	}
}
