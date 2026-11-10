package update

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ExtractZip unpacks the zip file src into dest (which must exist and should be empty) and returns the
// name of the single top-level folder every entry lives under. It extracts only regular files and
// folders, and rejects absolute paths, drive letters and UNC names, backslashes, "..", entries outside
// the top folder, symlinks and other special entries, duplicates, too many entries, and archives that
// expand beyond maxBytes (counted while copying, not from the sizes the archive declares).
func ExtractZip(src, dest string, maxBytes int64) (string, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return "", fmt.Errorf("not a zip file: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return "", fmt.Errorf("too many entries (more than %d)", maxEntries)
	}
	var top string
	left := maxBytes
	for _, zf := range zr.File {
		name := zf.Name
		if name == "" || strings.ContainsAny(name, "\\\x00:") || path.IsAbs(name) {
			return "", fmt.Errorf("unsafe entry name %q", name)
		}
		clean := path.Clean(name)
		if clean == "." {
			continue
		}
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return "", fmt.Errorf("entry %q escapes the archive", name)
		}
		mode := zf.Mode()
		if !mode.IsDir() && !mode.IsRegular() {
			return "", fmt.Errorf("entry %q is a symlink or special file, which releases never contain", name)
		}
		first, _, nested := strings.Cut(clean, "/")
		if top == "" {
			if !nested && !mode.IsDir() {
				return "", fmt.Errorf("entry %q is outside a top-level folder", name)
			}
			top = first
		} else if first != top {
			return "", fmt.Errorf("entry %q is outside the top folder %q", name, top)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if err := noSymlinkParents(dest, filepath.Dir(target)); err != nil {
			return "", fmt.Errorf("entry %q: %v", name, err)
		}
		if mode.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if zf.UncompressedSize64 > uint64(left) {
			return "", fmt.Errorf("archive expands beyond the %d MiB limit", maxBytes>>20)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		n, err := copyZipEntry(zf, target, left)
		if err != nil {
			return "", fmt.Errorf("entry %q: %v", name, err)
		}
		left -= n
	}
	if top == "" {
		return "", errors.New("archive is empty")
	}
	return top, nil
}

var errTooBig = errors.New("expands beyond the size limit")

func copyZipEntry(zf *zip.File, target string, limit int64) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(rc, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errTooBig
	}
	return n, err
}
