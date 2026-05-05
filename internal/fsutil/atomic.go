// Package fsutil provides filesystem helpers that wrap os primitives with
// stronger guarantees than the stdlib offers out of the box.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path by creating a uniquely-named temp file
// in the same directory and renaming it into place. Readers and concurrent
// writers see either the old contents or the new contents, never a partial or
// interleaved write. Last-writer-wins semantics still apply — this helper
// addresses byte-level corruption, not read-modify-write logical races.
//
// Equivalent in spirit to .NET's File.Replace / File.Move(overwrite: true).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing tmp: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
