package webdav

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

// CombinedFileSystem composes several webdav.Dir filesystems, keyed by
// their top-level path segment, into a single virtual root — so a WebDAV
// client can mount one URL and see each category (staging, retrieved,
// dryrun) as a subfolder, instead of needing a separate mount per
// category.
type CombinedFileSystem map[string]webdav.Dir

// NewCombinedFileSystem builds a CombinedFileSystem from category name
// (the subfolder a client will see) to the real directory it serves.
func NewCombinedFileSystem(dirs map[string]string) CombinedFileSystem {
	fs := make(CombinedFileSystem, len(dirs))
	for name, dir := range dirs {
		fs[name] = webdav.Dir(dir)
	}
	return fs
}

// CombinedHandler serves several directories under one WebDAV root, each
// as a subfolder named by its key in dirs.
func CombinedHandler(dirs map[string]string) http.Handler {
	return &webdav.Handler{
		Prefix:     "/",
		FileSystem: NewCombinedFileSystem(dirs),
		LockSystem: webdav.NewMemLS(),
	}
}

// split breaks a WebDAV path into its category (first segment) and the
// remainder (always slash-prefixed — "/" if nothing follows).
func (fs CombinedFileSystem) split(name string) (category, rest string, isRoot bool) {
	trimmed := strings.TrimPrefix(path.Clean("/"+name), "/")
	if trimmed == "" {
		return "", "", true
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 {
		return parts[0], "/" + parts[1], false
	}
	return parts[0], "/", false
}

func (fs CombinedFileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return os.ErrPermission
	}
	dir, ok := fs[category]
	if !ok {
		return os.ErrNotExist
	}
	return dir.Mkdir(ctx, rest, perm)
}

func (fs CombinedFileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		// The three categories are fixed, so the root only ever supports a
		// read-only directory listing — no writing/creating at this level.
		if flag != os.O_RDONLY {
			return nil, os.ErrPermission
		}
		return fs.rootFile(), nil
	}
	dir, ok := fs[category]
	if !ok {
		return nil, os.ErrNotExist
	}
	return dir.OpenFile(ctx, rest, flag, perm)
}

func (fs CombinedFileSystem) RemoveAll(ctx context.Context, name string) error {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return os.ErrPermission
	}
	dir, ok := fs[category]
	if !ok {
		return os.ErrNotExist
	}
	return dir.RemoveAll(ctx, rest)
}

func (fs CombinedFileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldCategory, oldRest, oldIsRoot := fs.split(oldName)
	newCategory, newRest, newIsRoot := fs.split(newName)
	if oldIsRoot || newIsRoot {
		return os.ErrPermission
	}
	// Moving a file between categories would mean copying across separate,
	// independently-configured real directories rather than a single
	// os.Rename — not a supported operation.
	if oldCategory != newCategory {
		return os.ErrPermission
	}
	dir, ok := fs[oldCategory]
	if !ok {
		return os.ErrNotExist
	}
	return dir.Rename(ctx, oldRest, newRest)
}

func (fs CombinedFileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	category, rest, isRoot := fs.split(name)
	if isRoot {
		return dirInfo{name: "/"}, nil
	}
	dir, ok := fs[category]
	if !ok {
		return nil, os.ErrNotExist
	}
	return dir.Stat(ctx, rest)
}

// rootFile lists this CombinedFileSystem's categories as the root
// directory's children.
func (fs CombinedFileSystem) rootFile() webdav.File {
	names := make([]string, 0, len(fs))
	for name := range fs {
		names = append(names, name)
	}
	sort.Strings(names)
	return &rootDir{names: names}
}

// dirInfo is a synthetic os.FileInfo for a directory that doesn't exist
// on any real filesystem — the combined root itself, or one of its
// category subfolders as seen in the root's own listing.
type dirInfo struct{ name string }

func (d dirInfo) Name() string     { return d.name }
func (dirInfo) Size() int64        { return 0 }
func (dirInfo) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (dirInfo) ModTime() time.Time { return time.Time{} }
func (dirInfo) IsDir() bool        { return true }
func (dirInfo) Sys() any           { return nil }

// rootDir is the webdav.File handle for the synthetic root: it only
// supports listing its children and Stat-ing itself. Read/Write/Seek on
// a directory handle are invalid, mirroring how *os.File behaves for the
// same operations against a real directory.
type rootDir struct {
	names  []string
	offset int
}

func (r *rootDir) Close() error                   { return nil }
func (r *rootDir) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (r *rootDir) Write([]byte) (int, error)      { return 0, os.ErrInvalid }
func (r *rootDir) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }
func (r *rootDir) Stat() (os.FileInfo, error)     { return dirInfo{name: "/"}, nil }

func (r *rootDir) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		infos := make([]os.FileInfo, len(r.names))
		for i, name := range r.names {
			infos[i] = dirInfo{name: name}
		}
		return infos, nil
	}
	if r.offset >= len(r.names) {
		return nil, io.EOF
	}
	end := r.offset + count
	if end > len(r.names) {
		end = len(r.names)
	}
	infos := make([]os.FileInfo, 0, end-r.offset)
	for _, name := range r.names[r.offset:end] {
		infos = append(infos, dirInfo{name: name})
	}
	r.offset = end
	return infos, nil
}
