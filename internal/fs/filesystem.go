package fs

import (
	"io"
	"os"
	"time"
)

// FileInfo is a protocol-agnostic file descriptor.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
}

// FileSystem is the common interface implemented by both local and remote
// (SFTP/FTP) backends. All paths are absolute on their respective filesystems.
type FileSystem interface {
	// Stat returns metadata for the file or directory at path.
	Stat(path string) (*FileInfo, error)

	// List returns the immediate children of a directory.
	List(path string) ([]*FileInfo, error)

	// Get opens path for reading. Caller must close the returned ReadCloser.
	Get(path string) (io.ReadCloser, error)

	// Put writes content to path, creating or truncating the file.
	// perm is applied when non-zero.
	Put(path string, content io.Reader, perm os.FileMode) error

	// MkdirAll creates dir and all required parents.
	MkdirAll(path string, perm os.FileMode) error

	// Remove deletes a single file or empty directory.
	Remove(path string) error

	// RemoveAll deletes path and everything beneath it.
	RemoveAll(path string) error

	// Rename moves oldPath to newPath.
	Rename(oldPath, newPath string) error

	// Chmod changes the mode of path.
	Chmod(path string, mode os.FileMode) error
}
