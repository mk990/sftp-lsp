package fs

import (
	"io"
	"os"
	"path/filepath"
)

// LocalFileSystem implements FileSystem for the local disk.
type LocalFileSystem struct{}

func NewLocal() *LocalFileSystem { return &LocalFileSystem{} }

func (l *LocalFileSystem) Stat(path string) (*FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return fromOsFileInfo(fi), nil
}

func (l *LocalFileSystem) List(path string) ([]*FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]*FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		result = append(result, fromOsFileInfo(info))
	}
	return result, nil
}

func (l *LocalFileSystem) Get(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (l *LocalFileSystem) Put(path string, content io.Reader, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, content)
	return err
}

func (l *LocalFileSystem) MkdirAll(path string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o755
	}
	return os.MkdirAll(path, perm)
}

func (l *LocalFileSystem) Remove(path string) error      { return os.Remove(path) }
func (l *LocalFileSystem) RemoveAll(path string) error   { return os.RemoveAll(path) }
func (l *LocalFileSystem) Rename(old, new string) error  { return os.Rename(old, new) }
func (l *LocalFileSystem) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func fromOsFileInfo(fi os.FileInfo) *FileInfo {
	return &FileInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime(),
		IsDir:   fi.IsDir(),
	}
}
