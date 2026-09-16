package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	h := Handler("/", dir)

	req := httptest.NewRequest(http.MethodPut, "/hello.txt", strings.NewReader("hi"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/hello.txt", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "hi" {
		t.Fatalf("GET = %d %q, want 200 \"hi\"", w.Code, w.Body.String())
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 12 {
		t.Errorf("DirSize = %d, want 12", size)
	}
}
