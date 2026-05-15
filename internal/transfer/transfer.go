package transfer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"sftp-lsp/internal/fs"
)

// Direction of a transfer.
type Direction int

const (
	Upload   Direction = iota // local → remote
	Download                  // remote → local
)

// Options controls a transfer operation.
type Options struct {
	Direction   Direction
	Concurrency int
	FilePerm    os.FileMode // 0 = use source mode
	DirPerm     os.FileMode // 0 = default (0755)
	UseTempFile bool
	DryRun      bool
	Ignore      []string // glob patterns relative to the base path
	// Progress is called after each file transfer.
	Progress func(localPath, remotePath string, size int64)
}

// Engine performs uploads and downloads between two FileSystems.
type Engine struct {
	local      fs.FileSystem
	remote     fs.FileSystem
	localBase  string // absolute local root
	remoteBase string // absolute remote root (slash-separated)
}

func NewEngine(localFS, remoteFS fs.FileSystem, localBase, remoteBase string) *Engine {
	return &Engine{
		local:      localFS,
		remote:     remoteFS,
		localBase:  filepath.Clean(localBase),
		remoteBase: path.Clean(remoteBase),
	}
}

// Transfer uploads or downloads a single file or directory tree.
// localPath and remotePath are the specific paths to transfer (may differ from bases).
func (e *Engine) Transfer(ctx context.Context, localPath, remotePath string, opts Options) error {
	fi, err := e.statSource(localPath, remotePath, opts.Direction)
	if err != nil {
		return err
	}
	if fi.IsDir {
		return e.transferDir(ctx, localPath, remotePath, opts)
	}
	return e.transferFile(ctx, localPath, remotePath, fi, opts)
}

// UploadFile uploads a single local file to the remote path.
func (e *Engine) UploadFile(ctx context.Context, localPath, remotePath string, opts Options) error {
	opts.Direction = Upload
	fi, err := e.local.Stat(localPath)
	if err != nil {
		return err
	}
	return e.transferFile(ctx, localPath, remotePath, fi, opts)
}

// DownloadFile downloads a single remote file to the local path.
func (e *Engine) DownloadFile(ctx context.Context, remotePath, localPath string, opts Options) error {
	opts.Direction = Download
	fi, err := e.remote.Stat(remotePath)
	if err != nil {
		return err
	}
	return e.transferFile(ctx, localPath, remotePath, fi, opts)
}

func (e *Engine) statSource(localPath, remotePath string, dir Direction) (*fs.FileInfo, error) {
	if dir == Upload {
		return e.local.Stat(localPath)
	}
	return e.remote.Stat(remotePath)
}

func (e *Engine) transferDir(ctx context.Context, localPath, remotePath string, opts Options) error {
	entries, err := e.listSource(localPath, remotePath, opts.Direction)
	if err != nil {
		return err
	}

	var tasks []Task
	for _, fi := range entries {
		fi := fi
		childLocal := filepath.Join(localPath, fi.Name)
		childRemote := path.Join(remotePath, fi.Name)

		if e.isIgnored(childLocal, opts.Ignore) {
			continue
		}

		tasks = append(tasks, func(ctx context.Context) error {
			return e.Transfer(ctx, childLocal, childRemote, opts)
		})
	}

	// Create destination directory.
	dirPerm := opts.DirPerm
	if dirPerm == 0 {
		dirPerm = 0o755
	}
	if !opts.DryRun {
		if opts.Direction == Upload {
			if err := e.remote.MkdirAll(remotePath, dirPerm); err != nil {
				return fmt.Errorf("mkdir remote %s: %w", remotePath, err)
			}
		} else {
			if err := e.local.MkdirAll(localPath, dirPerm); err != nil {
				return fmt.Errorf("mkdir local %s: %w", localPath, err)
			}
		}
	}

	sched := NewScheduler(opts.Concurrency)
	return sched.RunAll(ctx, tasks)
}

func (e *Engine) transferFile(ctx context.Context, localPath, remotePath string, srcInfo *fs.FileInfo, opts Options) error {
	if e.isIgnored(localPath, opts.Ignore) {
		return nil
	}
	if opts.DryRun {
		return nil
	}

	perm := opts.FilePerm
	if perm == 0 {
		perm = srcInfo.Mode & 0o777
	}

	if opts.Direction == Upload {
		return e.putFile(ctx, localPath, remotePath, perm, opts, srcInfo.Size)
	}
	return e.getFile(ctx, remotePath, localPath, perm, opts, srcInfo.Size)
}

func (e *Engine) putFile(_ context.Context, localPath, remotePath string, perm os.FileMode, opts Options, size int64) error {
	// Ensure remote parent directory exists.
	if err := e.remote.MkdirAll(path.Dir(remotePath), opts.DirPerm); err != nil {
		return fmt.Errorf("mkdir remote parent: %w", err)
	}

	r, err := e.local.Get(localPath)
	if err != nil {
		return fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer r.Close()

	dest := remotePath
	if opts.UseTempFile {
		dest = remotePath + ".sftptmp"
	}

	if err := e.remote.Put(dest, r, perm); err != nil {
		return fmt.Errorf("put remote %s: %w", dest, err)
	}

	if opts.UseTempFile {
		if err := e.remote.Rename(dest, remotePath); err != nil {
			_ = e.remote.Remove(dest)
			return fmt.Errorf("rename temp file: %w", err)
		}
	}

	if opts.Progress != nil {
		opts.Progress(localPath, remotePath, size)
	}
	return nil
}

func (e *Engine) getFile(_ context.Context, remotePath, localPath string, perm os.FileMode, opts Options, size int64) error {
	if err := e.local.MkdirAll(filepath.Dir(localPath), opts.DirPerm); err != nil {
		return fmt.Errorf("mkdir local parent: %w", err)
	}

	r, err := e.remote.Get(remotePath)
	if err != nil {
		return fmt.Errorf("get remote %s: %w", remotePath, err)
	}
	defer r.Close()

	var src io.Reader = r
	if err := e.local.Put(localPath, src, perm); err != nil {
		return fmt.Errorf("put local %s: %w", localPath, err)
	}

	if opts.Progress != nil {
		opts.Progress(localPath, remotePath, size)
	}
	return nil
}

func (e *Engine) listSource(localPath, remotePath string, dir Direction) ([]*fs.FileInfo, error) {
	if dir == Upload {
		return e.local.List(localPath)
	}
	return e.remote.List(remotePath)
}

func (e *Engine) isIgnored(localPath string, patterns []string) bool {
	rel, err := filepath.Rel(e.localBase, localPath)
	if err != nil {
		rel = localPath
	}
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(localPath)); matched {
			return true
		}
	}
	return false
}
