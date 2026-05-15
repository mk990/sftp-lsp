package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"sftp-lsp/internal/fs"
	"sftp-lsp/internal/transfer"
)

var watchDelete bool

var watchCmd = &cobra.Command{
	Use:   "watch [local-path]",
	Short: "Watch for local changes and upload them automatically",
	Long: `watch recursively monitors a local directory and uploads every file
that is created or modified. Saves are debounced (200 ms) so rapid
editor writes only trigger one upload.

Press Ctrl-C to stop watching.
`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPath, err := resolveLocalPath(args)
		if err != nil {
			return err
		}

		_, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return fmt.Errorf("create watcher: %w", err)
		}
		defer watcher.Close()

		// Add every existing directory recursively.
		if err := addDirsRecursive(watcher, localPath); err != nil {
			return err
		}

		// Merge config ignore patterns with .gitignore and ignoreFile.
		gitignorePatterns := loadIgnoreFile(filepath.Join(localPath, ".gitignore"))
		if cfg.IgnoreFile != "" {
			gitignorePatterns = append(gitignorePatterns, loadIgnoreFile(filepath.Join(localPath, cfg.IgnoreFile))...)
		}
		allIgnore := append(cfg.Ignore, gitignorePatterns...)

		localFS := fs.NewLocal()
		engine := transfer.NewEngine(localFS, c.FS(), localPath, cfg.RemotePath)
		opts := makeOpts(cfg, transfer.Upload)
		opts.Ignore = allIgnore

		// Debounce: collect events for 200 ms before acting.
		pending := make(map[string]fsnotify.Op)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		fmt.Printf("watching %s → %s (Ctrl-C to stop)\n", localPath, cfg.RemotePath)

		for {
			select {
			case <-ctx.Done():
				fmt.Println("\nstopped.")
				return nil

			case event, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				rel, _ := filepath.Rel(localPath, event.Name)
				if isIgnoredPath(event.Name, rel, allIgnore) {
					continue
				}
				pending[event.Name] |= event.Op

			case err, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
				fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)

			case <-ticker.C:
				if len(pending) == 0 {
					continue
				}
				batch := pending
				pending = make(map[string]fsnotify.Op)

				for path, op := range batch {
					// Pure removal (not followed by a recreate in the same debounce window).
					// Editors that do atomic writes (vim, neovim) emit Remove+Create for the
					// same path; those must fall through to the upload branch below.
					if op&(fsnotify.Remove|fsnotify.Rename) != 0 && op&(fsnotify.Create|fsnotify.Write) == 0 {
						if watchDelete {
							remotePath := localToRemote(path, localPath, cfg.RemotePath)
							if err := c.FS().RemoveAll(remotePath); err != nil {
								fmt.Fprintf(os.Stderr, "  ✗ delete %s: %v\n", remotePath, err)
							} else {
								fmt.Printf("  ✗ deleted %s\n", remotePath)
							}
						}
						continue
					}

					// Write or Create (including atomic-write Remove+Create pattern).
					fi, err := os.Stat(path)
					if err != nil {
						continue // file may have disappeared
					}

					remotePath := localToRemote(path, localPath, cfg.RemotePath)

					if fi.IsDir() {
						// New directory: add it and its subdirs to the watcher, mirror on remote.
						_ = addDirsRecursive(watcher, path)
						if !flagDryRun {
							_ = c.FS().MkdirAll(remotePath, os.FileMode(cfg.DirPerm))
						}
						fmt.Printf("  + dir  %s\n", remotePath)
						continue
					}

					if flagDryRun {
						fmt.Printf("  ~ (dry-run) would upload %s\n", path)
						continue
					}

					rel, _ := filepath.Rel(localPath, path)
					if err := engine.UploadFile(context.Background(), path, remotePath, opts); err != nil {
						if os.IsNotExist(err) {
							// Transient temp file — already gone before upload opened it.
							continue
						}
						fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", rel, err)
					} else {
						fmt.Printf("  ↑ local:%s → remote:%s (%d bytes)\n", rel, remotePath, fi.Size())
					}
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.Flags().BoolVar(&watchDelete, "delete", false,
		"delete remote file when local file is removed")
}

// addDirsRecursive adds localPath and every subdirectory to the watcher.
func addDirsRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}

// editorTempPatterns are transient files created by editors/formatters that
// should never be uploaded. They are typically written and deleted in under a
// second (conform.nvim, vim swapfiles, emacs lock files, etc.).
var editorTempPatterns = []string{
	".conform.*",        // conform.nvim formatter temp files
	"*.swp",             // vim swap
	"*.swo",             // vim swap
	"4913",              // vim atomic-write probe
	".#*",               // emacs lock symlinks
	"*~",                // emacs/gedit backup
	"*.tmp",
	"*.bak",
	".php-cs-fixer.cache",
	".php-cs-fixer.dist.cache",
}

// isIgnoredPath returns true if path matches any config ignore pattern or a
// built-in editor temp pattern. rel is the path relative to the watch root
// and enables matching directory-level patterns like "node_modules" or "dist".
func isIgnoredPath(path, rel string, patterns []string) bool {
	base := filepath.Base(path)
	relSlash := filepath.ToSlash(rel)
	all := append(editorTempPatterns, patterns...)
	for _, p := range all {
		// Match against basename (e.g. "*.swp", ".env")
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		// Match against each path component (e.g. "node_modules", "dist")
		for component := range strings.SplitSeq(relSlash, "/") {
			if ok, _ := filepath.Match(p, component); ok {
				return true
			}
		}
		// Match against the full relative path (e.g. "src/generated/*.go")
		if ok, _ := filepath.Match(p, relSlash); ok {
			return true
		}
	}
	return false
}

// loadIgnoreFile reads a .gitignore-style file and returns its patterns.
// Comment lines (#) and blank lines are skipped. Negation (!) is not supported.
func loadIgnoreFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var patterns []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		patterns = append(patterns, strings.TrimRight(line, "/"))
	}
	return patterns
}
