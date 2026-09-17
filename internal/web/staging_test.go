package web

import (
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"varis/internal/binpack"
	"varis/internal/db"
)

func TestGroupStagedFiles_RootFilesFirstThenAlphabeticalFolders(t *testing.T) {
	files := []binpack.FileInfo{
		{Path: "zzz-folder/x.txt", Size: 1},
		{Path: "root.txt", Size: 2},
		{Path: "afolder/a.txt", Size: 3},
		{Path: "afolder/b.txt", Size: 4},
	}
	meta := map[string]db.StagedMetadata{
		"root.txt": {Tags: []string{"tag1"}, Description: "desc"},
	}

	groups := groupStagedFiles(files, meta)

	if len(groups) != 3 {
		t.Fatalf("len(groups) = %d, want 3 (root, afolder, zzz-folder)", len(groups))
	}
	if groups[0].Name != "" || len(groups[0].Files) != 1 || groups[0].Files[0].Path != "root.txt" {
		t.Errorf("groups[0] = %+v, want the root group with root.txt", groups[0])
	}
	if groups[0].Files[0].Description != "desc" || !reflect.DeepEqual(groups[0].Files[0].Tags, []string{"tag1"}) {
		t.Errorf("groups[0].Files[0] = %+v, want metadata joined in", groups[0].Files[0])
	}
	if groups[1].Name != "afolder" || len(groups[1].Files) != 2 {
		t.Errorf("groups[1] = %+v, want afolder with 2 files", groups[1])
	}
	if groups[2].Name != "zzz-folder" || len(groups[2].Files) != 1 {
		t.Errorf("groups[2] = %+v, want zzz-folder with 1 file", groups[2])
	}
}

func TestGroupStagedFiles_UntaggedFileHasEmptyMetadata(t *testing.T) {
	files := []binpack.FileInfo{{Path: "a.txt", Size: 1}}
	groups := groupStagedFiles(files, map[string]db.StagedMetadata{})
	if len(groups) != 1 || len(groups[0].Files) != 1 {
		t.Fatalf("groups = %+v, want one group with one file", groups)
	}
	row := groups[0].Files[0]
	if row.Tags != nil || row.Description != "" {
		t.Errorf("row = %+v, want nil Tags and empty Description for an untagged file", row)
	}
}

func TestResolveSelectedPaths_DirectPathsAndFolderPrefixes(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("root.txt")
	mustWrite("vacation/a.jpg")
	mustWrite("vacation/b.jpg")
	mustWrite("work/report.pdf")

	got, err := resolveSelectedPaths(dir, []string{"root.txt", "vacation/"})
	if err != nil {
		t.Fatalf("resolveSelectedPaths: %v", err)
	}
	want := map[string]bool{"root.txt": true, "vacation/a.jpg": true, "vacation/b.jpg": true}
	if len(got) != len(want) {
		t.Fatalf("got = %v, want exactly %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q in result", p)
		}
	}
}

func TestTemplates_ParseWithoutError(t *testing.T) {
	if _, err := templatesTestParse(); err != nil {
		t.Fatalf("parsing embedded templates: %v", err)
	}
}

func templatesTestParse() (*template.Template, error) {
	return template.ParseFS(templatesFS, "templates/*.html")
}
