package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCombinedFileSystem_RootListsCategories(t *testing.T) {
	fs := NewCombinedFileSystem(map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
		"dryrun":    t.TempDir(),
	})

	f, err := fs.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(/): %v", err)
	}
	defer f.Close()

	entries, err := f.Readdir(0)
	if err != nil {
		t.Fatalf("Readdir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("entry %q: IsDir() = false, want true", e.Name())
		}
		names[e.Name()] = true
	}
	for _, want := range []string{"staging", "retrieved", "dryrun"} {
		if !names[want] {
			t.Errorf("root listing = %v, want it to include %q", names, want)
		}
	}
}

func TestCombinedFileSystem_RootReaddirPaginatesAndExhausts(t *testing.T) {
	fs := NewCombinedFileSystem(map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
		"dryrun":    t.TempDir(),
	})

	t.Run("partial then remainder", func(t *testing.T) {
		f, err := fs.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("OpenFile(/): %v", err)
		}
		defer f.Close()

		first, err := f.Readdir(1)
		if err != nil {
			t.Fatalf("Readdir(1): %v", err)
		}
		if len(first) != 1 {
			t.Fatalf("Readdir(1) returned %d entries, want 1", len(first))
		}

		rest, err := f.Readdir(0)
		if err != nil {
			t.Fatalf("Readdir(0): %v", err)
		}
		if len(rest) != 2 {
			t.Fatalf("Readdir(0) after Readdir(1) returned %d entries, want 2 (the remainder, not all 3 again)", len(rest))
		}
	})

	t.Run("repeated Readdir(0) does not re-yield entries", func(t *testing.T) {
		f, err := fs.OpenFile(context.Background(), "/", os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("OpenFile(/): %v", err)
		}
		defer f.Close()

		all, err := f.Readdir(0)
		if err != nil {
			t.Fatalf("first Readdir(0): %v", err)
		}
		if len(all) != 3 {
			t.Fatalf("first Readdir(0) returned %d entries, want 3", len(all))
		}

		again, err := f.Readdir(0)
		if err != nil && err != io.EOF {
			t.Fatalf("second Readdir(0): %v", err)
		}
		if len(again) != 0 {
			t.Errorf("second Readdir(0) returned %d entries, want 0 (already exhausted)", len(again))
		}
	})
}

func TestCombinedFileSystem_RejectsRootLevelMkdirAndRemoveAll(t *testing.T) {
	fs := NewCombinedFileSystem(map[string]string{"staging": t.TempDir()})

	if err := fs.Mkdir(context.Background(), "/", 0o755); err == nil {
		t.Error("Mkdir(/) = nil error, want an error (categories are fixed)")
	}
	if err := fs.RemoveAll(context.Background(), "/"); err == nil {
		t.Error("RemoveAll(/) = nil error, want an error (categories are fixed)")
	}
}

func TestCombinedFileSystem_RejectsCrossCategoryRename(t *testing.T) {
	fs := NewCombinedFileSystem(map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
	})

	f, err := fs.OpenFile(context.Background(), "/staging/a.txt", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.Close()

	if err := fs.Rename(context.Background(), "/staging/a.txt", "/retrieved/a.txt"); err == nil {
		t.Error("Rename across categories = nil error, want an error")
	}
}

func TestCombinedHandler_PutGetDeleteRoundTripsWithinEachCategory(t *testing.T) {
	dirs := map[string]string{
		"staging":   t.TempDir(),
		"retrieved": t.TempDir(),
		"dryrun":    t.TempDir(),
	}
	h := CombinedHandler(dirs)

	for category, dir := range dirs {
		t.Run(category, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/"+category+"/hello.txt", strings.NewReader("hi"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("PUT status = %d, want 201", w.Code)
			}

			req = httptest.NewRequest(http.MethodGet, "/"+category+"/hello.txt", nil)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != "hi" {
				t.Fatalf("GET = %d %q, want 200 \"hi\"", w.Code, w.Body.String())
			}

			if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err != nil {
				t.Errorf("expected hello.txt on disk at %s: %v", dir, err)
			}

			req = httptest.NewRequest(http.MethodDelete, "/"+category+"/hello.txt", nil)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("DELETE status = %d, want 204", w.Code)
			}
		})
	}
}

func TestCombinedHandler_UnknownCategoryNotFoundOnGet(t *testing.T) {
	h := CombinedHandler(map[string]string{"staging": t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/nope/x.txt", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /nope/x.txt status = %d, want 404", w.Code)
	}
}

func TestCombinedHandler_RejectsCreatingATopLevelCategory(t *testing.T) {
	h := CombinedHandler(map[string]string{"staging": t.TempDir()})

	req := httptest.NewRequest(http.MethodPut, "/newcategory", strings.NewReader("hi"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// "newcategory" isn't a known category, so the underlying webdav.Handler
	// treats this like writing into a nonexistent parent directory: 409
	// Conflict, not 201 Created. The three categories are fixed; a client
	// can never create a fourth by PUTting at the root.
	if w.Code != http.StatusConflict {
		t.Errorf("PUT /newcategory status = %d, want 409", w.Code)
	}
}
