package transfer

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"time"

	"sftp-lsp/internal/fs"
)

// SyncDirection controls which side wins during a sync.
type SyncDirection int

const (
	SyncLocalToRemote SyncDirection = iota
	SyncRemoteToLocal
	SyncBothDirections // newest file wins
)

// SyncOptions controls the sync behaviour.
type SyncOptions struct {
	Direction      SyncDirection
	Concurrency    int
	Delete         bool        // remove extraneous files from destination
	SkipCreate     bool        // don't create new files on destination
	IgnoreExisting bool        // don't overwrite existing destination files
	Update         bool        // only overwrite when source is newer
	FilePerm       os.FileMode // 0 = source mode
	DirPerm        os.FileMode // 0 = 0755
	Ignore         []string    // glob patterns
	// RemoteTimeOffset adjusts remote mtime for comparison (hours).
	RemoteTimeOffset time.Duration
	Progress         func(localPath, remotePath string, size int64)
}

// SyncResult summarises what was done.
type SyncResult struct {
	Transferred int
	Deleted     int
	Skipped     int
}

// Sync compares two directory trees and brings them in line according to opts.
func Sync(ctx context.Context, engine *Engine, localDir, remoteDir string, opts SyncOptions) (*SyncResult, error) {
	result := &SyncResult{}
	return result, syncDir(ctx, engine, localDir, remoteDir, opts, result)
}

func syncDir(ctx context.Context, engine *Engine, localDir, remoteDir string, opts SyncOptions, result *SyncResult) error {
	localEntries, err := safeList(engine.local, localDir)
	if err != nil {
		return err
	}
	remoteEntries, err := safeList(engine.remote, remoteDir)
	if err != nil {
		return err
	}

	localMap := indexByName(localEntries)
	remoteMap := indexByName(remoteEntries)

	var tasks []Task

	// Entries that exist on the source side.
	switch opts.Direction {
	case SyncLocalToRemote:
		for name, lfi := range localMap {
			lfi, rfi := lfi, remoteMap[name]
			localPath := filepath.Join(localDir, name)
			remotePath := path.Join(remoteDir, name)

			if engine.isIgnored(localPath, opts.Ignore) {
				result.Skipped++
				continue
			}

			if lfi.IsDir {
				dirPerm := opts.DirPerm
				if dirPerm == 0 {
					dirPerm = 0o755
				}
				if err := engine.remote.MkdirAll(remotePath, 0); err != nil {
					return err
				}
				if err := syncDir(ctx, engine, localPath, remotePath, opts, result); err != nil {
					return err
				}
				continue
			}

			action := decideAction(lfi, rfi, opts)
			if action == actionSkip {
				result.Skipped++
				continue
			}

			tasks = append(tasks, func(ctx context.Context) error {
				tOpts := Options{
					Direction:   Upload,
					Concurrency: opts.Concurrency,
					FilePerm:    0,
					Progress:    opts.Progress,
				}
				if err := engine.UploadFile(ctx, localPath, remotePath, tOpts); err != nil {
					return err
				}
				result.Transferred++
				return nil
			})
		}

		// Delete remote files not present locally.
		if opts.Delete {
			for name, rfi := range remoteMap {
				if _, exists := localMap[name]; exists {
					continue
				}
				remotePath := path.Join(remoteDir, name)
				rfi := rfi
				tasks = append(tasks, func(ctx context.Context) error {
					var err error
					if rfi.IsDir {
						err = engine.remote.RemoveAll(remotePath)
					} else {
						err = engine.remote.Remove(remotePath)
					}
					if err != nil {
						return err
					}
					result.Deleted++
					return nil
				})
			}
		}

	case SyncRemoteToLocal:
		for name, rfi := range remoteMap {
			rfi, lfi := rfi, localMap[name]
			remotePath := path.Join(remoteDir, name)
			localPath := filepath.Join(localDir, name)

			if rfi.IsDir {
				if err := engine.local.MkdirAll(localPath, 0); err != nil {
					return err
				}
				if err := syncDir(ctx, engine, localPath, remotePath, opts, result); err != nil {
					return err
				}
				continue
			}

			action := decideAction(rfi, lfi, opts)
			if action == actionSkip {
				result.Skipped++
				continue
			}

			tasks = append(tasks, func(ctx context.Context) error {
				tOpts := Options{
					Direction:   Download,
					Concurrency: opts.Concurrency,
					Progress:    opts.Progress,
				}
				if err := engine.DownloadFile(ctx, remotePath, localPath, tOpts); err != nil {
					return err
				}
				result.Transferred++
				return nil
			})
		}

		if opts.Delete {
			for name, lfi := range localMap {
				if _, exists := remoteMap[name]; exists {
					continue
				}
				localPath := filepath.Join(localDir, name)
				lfi := lfi
				tasks = append(tasks, func(ctx context.Context) error {
					var err error
					if lfi.IsDir {
						err = engine.local.RemoveAll(localPath)
					} else {
						err = engine.local.Remove(localPath)
					}
					if err != nil {
						return err
					}
					result.Deleted++
					return nil
				})
			}
		}

	case SyncBothDirections:
		// Newest file wins; ensure both sides have all files.
		allNames := make(map[string]bool)
		for n := range localMap {
			allNames[n] = true
		}
		for n := range remoteMap {
			allNames[n] = true
		}

		for name := range allNames {
			name := name
			lfi := localMap[name]
			rfi := remoteMap[name]
			localPath := filepath.Join(localDir, name)
			remotePath := path.Join(remoteDir, name)

			tasks = append(tasks, func(ctx context.Context) error {
				tOpts := Options{Concurrency: opts.Concurrency, Progress: opts.Progress}
				switch {
				case lfi == nil:
					// Only on remote → download.
					tOpts.Direction = Download
					if err := engine.DownloadFile(ctx, remotePath, localPath, tOpts); err != nil {
						return err
					}
				case rfi == nil:
					// Only local → upload.
					tOpts.Direction = Upload
					if err := engine.UploadFile(ctx, localPath, remotePath, tOpts); err != nil {
						return err
					}
				default:
					// Both exist — transfer whichever is newer.
					if lfi.ModTime.After(rfi.ModTime.Add(opts.RemoteTimeOffset)) {
						tOpts.Direction = Upload
						if err := engine.UploadFile(ctx, localPath, remotePath, tOpts); err != nil {
							return err
						}
					} else if rfi.ModTime.Add(opts.RemoteTimeOffset).After(lfi.ModTime) {
						tOpts.Direction = Download
						if err := engine.DownloadFile(ctx, remotePath, localPath, tOpts); err != nil {
							return err
						}
					}
				}
				result.Transferred++
				return nil
			})
		}
	}

	sched := NewScheduler(opts.Concurrency)
	return sched.RunAll(ctx, tasks)
}

type action int

const (
	actionTransfer action = iota
	actionSkip
)

func decideAction(src, dst *fs.FileInfo, opts SyncOptions) action {
	if dst == nil {
		if opts.SkipCreate {
			return actionSkip
		}
		return actionTransfer
	}
	if opts.IgnoreExisting {
		return actionSkip
	}
	if opts.Update && !src.ModTime.After(dst.ModTime) {
		return actionSkip
	}
	return actionTransfer
}

func safeList(fsys fs.FileSystem, dir string) ([]*fs.FileInfo, error) {
	entries, err := fsys.List(dir)
	if err != nil {
		// Treat a missing directory as empty.
		return nil, nil
	}
	return entries, nil
}

func indexByName(entries []*fs.FileInfo) map[string]*fs.FileInfo {
	m := make(map[string]*fs.FileInfo, len(entries))
	for _, e := range entries {
		m[e.Name] = e
	}
	return m
}
