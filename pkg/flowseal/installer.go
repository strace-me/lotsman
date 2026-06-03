package flowseal

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileInstaller is the real Installer: it extracts a release zip into a
// versioned directory and repoints a stable symlink (flowseal-current) at it,
// so strategy scripts that reference the symlink pick up the new bundle on the
// next engine restart. Extraction is pure Go (archive/zip) — no external unzip.
type FileInstaller struct {
	Base        string // parent dir, e.g. /opt
	DirPrefix   string // versioned dir prefix, e.g. "flowseal-"
	CurrentLink string // stable symlink, e.g. /opt/flowseal-current
}

// NewFileInstaller builds an installer with the R5S layout.
func NewFileInstaller(base string) *FileInstaller {
	return &FileInstaller{
		Base:        base,
		DirPrefix:   "flowseal-",
		CurrentLink: filepath.Join(base, "flowseal-current"),
	}
}

// CurrentVersion reads the installed version from the symlink target's name, or
// "" if nothing is installed.
func (i *FileInstaller) CurrentVersion() string {
	target, err := os.Readlink(i.CurrentLink)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(target), i.DirPrefix)
}

// Install extracts the release zip into <Base>/<DirPrefix><tag> and repoints the
// current symlink at it.
func (i *FileInstaller) Install(_ context.Context, rel Release, raw []byte) error {
	dir := filepath.Join(i.Base, i.DirPrefix+rel.Tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := unzip(raw, dir); err != nil {
		return err
	}
	// Repoint symlink atomically: write a temp link then rename over the old one.
	tmp := i.CurrentLink + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(dir, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, i.CurrentLink)
}

// unzip extracts a zip archive into dir, rejecting path-traversal entries.
func unzip(raw []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("flowseal: open zip: %w", err)
	}
	for _, f := range zr.File {
		dest := filepath.Join(dir, f.Name)
		// zip-slip guard: dest must stay within dir.
		if !strings.HasPrefix(dest, filepath.Clean(dir)+string(os.PathSeparator)) && dest != filepath.Clean(dir) {
			return fmt.Errorf("flowseal: unsafe zip path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, dest); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}
