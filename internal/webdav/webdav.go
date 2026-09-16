package webdav

import (
	"io/fs"
	"net/http"
	"path/filepath"

	"golang.org/x/net/webdav"
)

// Handler serves dir as a WebDAV share.
func Handler(prefix, dir string) http.Handler {
	return &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	}
}

// DirSize returns the total size in bytes of all regular files under dir.
func DirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
