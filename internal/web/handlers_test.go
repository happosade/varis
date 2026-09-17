package web

import (
	"context"
	"html/template"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"varis/internal/burn"
	"varis/internal/db"
	"varis/internal/execx"
	"varis/internal/retrieve"
)

func TestSearch_EmptyQueryReturnsNoRows(t *testing.T) {
	// This only exercises the empty-query short-circuit, which needs no
	// database — everything else in this package is thin enough (a
	// one-line call into db/burn/retrieve, already tested in their own
	// packages) that handler-level tests would mostly re-test those
	// packages through an HTTP wrapper, which isn't worth the added
	// httptest+template plumbing at this project's scale.
	tmpl := template.Must(template.New("search-rows").Parse(`{{define "search-rows"}}{{len .}} rows{{end}}`))
	s := &Server{tmpl: tmpl}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/search?q=", nil)
	s.search(w, r)

	if !strings.Contains(w.Body.String(), "0 rows") {
		t.Errorf("body = %q, want it to contain '0 rows'", w.Body.String())
	}
}

// stubCataloger/stubCatalog below are minimal Cataloger/Catalog
// implementations that always succeed, just enough to get a Manager into
// a non-DONE/FAILED state for these lock tests — they don't need to
// exercise real burn/retrieve behavior, which is already covered in
// internal/burn and internal/retrieve.

type stubCataloger struct{ n int }

func (c *stubCataloger) NextDiskID(ctx context.Context, prefix string, alreadyAllocated int) (string, error) {
	c.n++
	return prefix + ":stub", nil
}
func (c *stubCataloger) NewBurnJobID(ctx context.Context) (string, error) { return "job", nil }
func (c *stubCataloger) NextGroupID(ctx context.Context, burnJobID string, groupSize int) (string, error) {
	return "group", nil
}
func (c *stubCataloger) InsertDisk(ctx context.Context, d db.Disk) error        { return nil }
func (c *stubCataloger) InsertFile(ctx context.Context, f db.FileRecord) error { return nil }

func TestStartBurn_RejectedWhileRetrievalInProgress(t *testing.T) {
	staging := t.TempDir()
	if err := writeFixtureFile(staging, "a.bin", "some bytes"); err != nil {
		t.Fatal(err)
	}
	burnMgr := burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device")

	retrieveMgr := retrieve.NewManager(stubCatalog{
		files: map[string]db.FileRecord{"f1": {ID: "f1", DiskID: "BD:0001", OriginalPath: "a.bin"}},
	}, &execx.FakeExecutor{}, t.TempDir(), t.TempDir(), t.TempDir()+"/device")
	if err := retrieveMgr.Start(context.Background(), "f1"); err != nil {
		t.Fatalf("retrieveMgr.Start: %v", err)
	}

	s := &Server{burnMgr: burnMgr, retrieveMgr: retrieveMgr}
	form := url.Values{"media_type": {"BD-R"}, "parity_percent": {"10"}}
	r := httptest.NewRequest("POST", "/burn", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	s.startBurn(w, r)

	if !strings.Contains(w.Body.String(), "retrieval") {
		t.Errorf("body = %q, want a rejection mentioning the in-progress retrieval", w.Body.String())
	}
	if burnMgr.Current() != nil {
		t.Error("expected Start to be rejected before a Job was created")
	}
}

func TestStartRetrieve_RejectedWhileBurnInProgress(t *testing.T) {
	staging := t.TempDir()
	if err := writeFixtureFile(staging, "a.bin", "some bytes"); err != nil {
		t.Fatal(err)
	}
	burnMgr := burn.NewManager(&stubCataloger{}, &execx.FakeExecutor{}, staging, t.TempDir(), t.TempDir()+"/device")
	if err := burnMgr.Start(context.Background(), burn.Options{MediaType: "BD-R", CapacityBytes: 1000, ParityPercent: 10}); err != nil {
		t.Fatalf("burnMgr.Start: %v", err)
	}

	retrieveMgr := retrieve.NewManager(stubCatalog{
		files: map[string]db.FileRecord{"f1": {ID: "f1", DiskID: "BD:0001", OriginalPath: "a.bin"}},
	}, &execx.FakeExecutor{}, t.TempDir(), t.TempDir(), t.TempDir()+"/device")

	s := &Server{burnMgr: burnMgr, retrieveMgr: retrieveMgr}
	form := url.Values{"file_id": {"f1"}}
	r := httptest.NewRequest("POST", "/retrieve", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	s.startRetrieve(w, r)

	if w.Result().StatusCode != 409 {
		t.Errorf("status = %d, want 409", w.Result().StatusCode)
	}
}

type stubCatalog struct {
	files map[string]db.FileRecord
}

func (c stubCatalog) GetFile(ctx context.Context, fileID string) (db.FileRecord, error) {
	return c.files[fileID], nil
}
func (c stubCatalog) GetDisk(ctx context.Context, diskID string) (db.Disk, error) { return db.Disk{}, nil }
func (c stubCatalog) GroupMembers(ctx context.Context, groupID string) ([]db.Disk, error) {
	return nil, nil
}
func (c stubCatalog) MediaCapacity(ctx context.Context, mediaType string) (int64, error) {
	return 1000, nil
}

func writeFixtureFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
