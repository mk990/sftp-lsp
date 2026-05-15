package client

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	goftp "github.com/jlaffaye/ftp"

	"sftp-lsp/internal/config"
	"sftp-lsp/internal/fs"
)

// FTPClient connects over FTP/FTPS.
type FTPClient struct {
	cfg        *config.Config
	conn       *goftp.ServerConn
	fileSystem *ftpFileSystem
}

func NewFTPClient(cfg *config.Config) *FTPClient {
	return &FTPClient{cfg: cfg}
}

func (c *FTPClient) Connect() error {
	opts := []goftp.DialOption{
		goftp.DialWithTimeout(time.Duration(c.cfg.ConnectTimeout) * time.Millisecond),
	}

	// FTPS (implicit TLS on port 990 or explicit via AUTH TLS).
	if secure, ok := c.cfg.Secure.(bool); ok && secure {
		opts = append(opts, goftp.DialWithExplicitTLS(nil))
	} else if s, ok := c.cfg.Secure.(string); ok {
		switch s {
		case "implicit":
			opts = append(opts, goftp.DialWithTLS(nil))
		case "control", "true":
			opts = append(opts, goftp.DialWithExplicitTLS(nil))
		}
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	conn, err := goftp.Dial(addr, opts...)
	if err != nil {
		return fmt.Errorf("ftp dial %s: %w", addr, err)
	}

	if err := conn.Login(c.cfg.Username, c.cfg.Password); err != nil {
		conn.Quit()
		return fmt.Errorf("ftp login: %w", err)
	}

	c.conn = conn
	c.fileSystem = &ftpFileSystem{conn: conn}
	return nil
}

func (c *FTPClient) Close() error {
	if c.conn != nil {
		return c.conn.Quit()
	}
	return nil
}

func (c *FTPClient) FS() fs.FileSystem { return c.fileSystem }

// ftpFileSystem adapts jlaffaye/ftp to the fs.FileSystem interface.
type ftpFileSystem struct {
	conn *goftp.ServerConn
}

func (f *ftpFileSystem) Stat(p string) (*fs.FileInfo, error) {
	entries, err := f.conn.List(path.Dir(p))
	if err != nil {
		return nil, err
	}
	name := path.Base(p)
	for _, e := range entries {
		if e.Name == name {
			return ftpEntryToFS(e), nil
		}
	}
	return nil, fmt.Errorf("%s: no such file or directory", p)
}

func (f *ftpFileSystem) List(p string) ([]*fs.FileInfo, error) {
	entries, err := f.conn.List(p)
	if err != nil {
		return nil, err
	}
	result := make([]*fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		result = append(result, ftpEntryToFS(e))
	}
	return result, nil
}

func (f *ftpFileSystem) Get(p string) (io.ReadCloser, error) {
	return f.conn.Retr(p)
}

func (f *ftpFileSystem) Put(p string, content io.Reader, _ os.FileMode) error {
	return f.conn.Stor(p, content)
}

func (f *ftpFileSystem) MkdirAll(p string, _ os.FileMode) error {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	current := "/"
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		if err := f.conn.MakeDir(current); err != nil {
			// Ignore "already exists" errors (550).
			if !strings.Contains(err.Error(), "550") {
				return err
			}
		}
	}
	return nil
}

func (f *ftpFileSystem) Remove(p string) error { return f.conn.Delete(p) }

func (f *ftpFileSystem) RemoveAll(p string) error {
	entries, err := f.conn.List(p)
	if err != nil {
		// May already not exist.
		return nil
	}
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		child := path.Join(p, e.Name)
		if e.Type == goftp.EntryTypeFolder {
			if err := f.RemoveAll(child); err != nil {
				return err
			}
		} else {
			if err := f.conn.Delete(child); err != nil {
				return err
			}
		}
	}
	return f.conn.RemoveDir(p)
}

func (f *ftpFileSystem) Rename(old, new string) error { return f.conn.Rename(old, new) }

// FTP has no chmod — silently succeed.
func (f *ftpFileSystem) Chmod(_ string, _ os.FileMode) error { return nil }

func ftpEntryToFS(e *goftp.Entry) *fs.FileInfo {
	return &fs.FileInfo{
		Name:    e.Name,
		Size:    int64(e.Size),
		Mode:    0o644,
		ModTime: e.Time,
		IsDir:   e.Type == goftp.EntryTypeFolder,
	}
}

// Ensure compile-time interface compliance.
var _ fs.FileSystem = (*ftpFileSystem)(nil)

// keepalive sends NOOPs at the given interval to prevent connection drops.
func (c *FTPClient) startKeepalive(interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if c.conn == nil {
				return
			}
			_ = c.conn.NoOp()
		}
	}()
}
