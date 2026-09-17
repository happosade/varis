package web

import (
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	want := []string{"root.txt", "vacation/a.jpg", "vacation/b.jpg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"family, 2019", []string{"family", "2019"}},
		{"family, ,2019", []string{"family", "2019"}},
		{"", nil},
		{"   ", nil},
		{"onlyone", []string{"onlyone"}},
	}
	for _, tt := range tests {
		if got := parseTags(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseTags(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

// TestStagingFilesFragment_Renders executes (not just parses) the real
// embedded "staging-files-fragment" template against representative data —
// template.Parse alone accepts a reference to a field that doesn't exist on
// stagedFileRow/stagedFolderGroup; only Execute catches that, so this is
// the test that actually protects the dashboard from a silent runtime 500
// on a struct field rename. It also doubles as an escaping check: a tag
// containing HTML-special characters must come out escaped.
func TestStagingFilesFragment_Renders(t *testing.T) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parsing embedded templates: %v", err)
	}
	groups := []stagedFolderGroup{
		{Name: "", Files: []stagedFileRow{{Path: "root.txt", Size: 1}}},
		{Name: "vacation", Files: []stagedFileRow{
			{Path: "vacation/a.jpg", Size: 2, Tags: []string{"<b>family</b>"}, Description: "a trip"},
		}},
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "staging-files-fragment", groups); err != nil {
		t.Fatalf("executing staging-files-fragment: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<b>family</b>") {
		t.Error("expected the tag's HTML-special characters to be escaped, found them raw")
	}
	if !strings.Contains(out, "&lt;b&gt;family&lt;/b&gt;") {
		t.Errorf("expected the escaped tag text in output, got:\n%s", out)
	}
	if !strings.Contains(out, "vacation/a.jpg") || !strings.Contains(out, "root.txt") {
		t.Errorf("expected both files' paths in output, got:\n%s", out)
	}
}
