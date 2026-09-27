// Package atomicfile is the ONE way this repository writes a file that a reader
// must never see half-written. Every implementation here did temp-file-plus-rename
// on its own, and three of the four used a FIXED "path.tmp" name, so two writers
// of the same file would interleave into the same temporary and one would rename
// the other's bytes into place.
//
// The temporary is created in the TARGET's directory with a random name: a rename
// across filesystems is not atomic, so a temp in /tmp would turn every write into
// a copy. The data is fsynced before the rename and the directory after it, so a
// power cut leaves either the old file or the new one, never a truncated mixture.
package atomicfile

import (
	"os"
	"path/filepath"
)

func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir makes the rename itself durable. Without it the rename can be lost
// after a crash even though the file's contents were flushed, which leaves a
// config that was updated and then silently reverted.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	// A filesystem that refuses to sync a directory is not a failure of the write.
	_ = handle.Sync()
	return nil
}
