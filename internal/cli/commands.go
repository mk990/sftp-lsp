package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sftp-lsp/internal/client"
	"sftp-lsp/internal/config"
	"sftp-lsp/internal/fs"
	"sftp-lsp/internal/transfer"
)

var (
	flagConfig  string
	flagProfile string
	flagDryRun  bool
)

// Execute runs the CLI root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "sftp-lsp",
	Short: "SFTP/FTP sync tool (CLI mode)",
	Long: `sftp-lsp --cli <command> [flags]

Reads .vscode/sftp.json from the current directory (or --config).
`,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "path to sftp.json (default: auto-discover .vscode/sftp.json)")
	rootCmd.PersistentFlags().StringVarP(&flagProfile, "profile", "p", "", "named profile to use (default: first)")
	rootCmd.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "show what would be done without doing it")

	rootCmd.AddCommand(uploadCmd, downloadCmd, syncCmd, listCmd, deleteCmd, mkdirCmd, configCmd)
}

// ---- upload ----

var uploadCmd = &cobra.Command{
	Use:   "upload [local-path]",
	Short: "Upload a file or directory to the remote server",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPath, err := resolveLocalPath(args)
		if err != nil {
			return err
		}
		workspaceRoot, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		remotePath := localToRemote(localPath, workspaceRoot, cfg.RemotePath)
		engine := transfer.NewEngine(fs.NewLocal(), c.FS(), workspaceRoot, cfg.RemotePath)
		opts := makeOpts(cfg, transfer.Upload)
		opts.DryRun = flagDryRun
		opts.Progress = progressPrinter()

		fmt.Printf("uploading %s → %s\n", localPath, remotePath)
		fi, err := os.Stat(localPath)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return engine.Transfer(context.Background(), localPath, remotePath, opts)
		}
		return engine.UploadFile(context.Background(), localPath, remotePath, opts)
	},
}

// ---- download ----

var downloadCmd = &cobra.Command{
	Use:   "download <remote-path> [local-path]",
	Short: "Download a file or directory from the remote server",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		workspaceRoot, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		remotePath := resolveRemotePath(args[0], cfg.RemotePath)

		localPath := ""
		if len(args) == 2 {
			localPath, err = filepath.Abs(args[1])
			if err != nil {
				return err
			}
		} else {
			localPath = defaultLocalDestination(remotePath, cfg.RemotePath, workspaceRoot)
		}

		engine := transfer.NewEngine(fs.NewLocal(), c.FS(), workspaceRoot, cfg.RemotePath)
		opts := makeOpts(cfg, transfer.Download)
		opts.DryRun = flagDryRun
		opts.Progress = progressPrinter()

		fmt.Printf("downloading %s → %s\n", remotePath, localPath)
		fi, err := c.FS().Stat(remotePath)
		if err != nil {
			return err
		}
		if fi.IsDir {
			return engine.Transfer(context.Background(), localPath, remotePath, opts)
		}
		return engine.DownloadFile(context.Background(), remotePath, localPath, opts)
	},
}

// ---- sync ----

var (
	syncDirection string
	syncDelete    bool
)

var syncCmd = &cobra.Command{
	Use:   "sync [local-path]",
	Short: "Sync between local and remote",
	Long: `sync --direction local-to-remote|remote-to-local|both [local-path]

Directions:
  local-to-remote  (default) push local changes to remote
  remote-to-local  pull remote changes to local
  both             newest file wins on each side
`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPath, err := resolveLocalPath(args)
		if err != nil {
			return err
		}
		workspaceRoot, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		remoteRoot := localToRemote(localPath, workspaceRoot, cfg.RemotePath)
		engine := transfer.NewEngine(fs.NewLocal(), c.FS(), workspaceRoot, cfg.RemotePath)

		dir := transfer.SyncLocalToRemote
		switch strings.ToLower(syncDirection) {
		case "remote-to-local", "rtl":
			dir = transfer.SyncRemoteToLocal
		case "both":
			dir = transfer.SyncBothDirections
		}

		opts := transfer.SyncOptions{
			Direction:        dir,
			Concurrency:      cfg.Concurrency,
			Delete:           syncDelete || cfg.SyncOption.Delete,
			SkipCreate:       cfg.SyncOption.SkipCreate,
			IgnoreExisting:   cfg.SyncOption.IgnoreExisting,
			Update:           cfg.SyncOption.Update,
			FilePerm:         os.FileMode(cfg.FilePerm),
			DirPerm:          os.FileMode(cfg.DirPerm),
			Ignore:           cfg.Ignore,
			RemoteTimeOffset: time.Duration(float64(time.Hour) * cfg.RemoteTimeOffsetHours),
			Progress:         progressPrinter(),
		}

		fmt.Printf("syncing %s ↔ %s\n", localPath, remoteRoot)
		result, err := transfer.Sync(context.Background(), engine, localPath, remoteRoot, opts)
		if err != nil {
			return err
		}
		fmt.Printf("sync complete: %d transferred, %d deleted, %d skipped\n",
			result.Transferred, result.Deleted, result.Skipped)
		return nil
	},
}

// ---- list ----

var listCmd = &cobra.Command{
	Use:   "list [remote-path]",
	Short: "List files on the remote server",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		remotePath := cfg.RemotePath
		if len(args) == 1 {
			remotePath = resolveRemotePath(args[0], cfg.RemotePath)
		}

		entries, err := c.FS().List(remotePath)
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "MODE\tSIZE\tMOD TIME\tNAME")
		for _, e := range entries {
			name := e.Name
			if e.IsDir {
				name += "/"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n",
				e.Mode.String(), e.Size, e.ModTime.Format(time.RFC3339), name)
		}
		return w.Flush()
	},
}

// ---- delete ----

var deleteCmd = &cobra.Command{
	Use:   "delete <remote-path>",
	Short: "Delete a file or directory on the remote server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		remotePath := resolveRemotePath(args[0], cfg.RemotePath)
		fi, err := c.FS().Stat(remotePath)
		if err != nil {
			return err
		}
		if flagDryRun {
			fmt.Printf("would delete %s\n", remotePath)
			return nil
		}
		if fi.IsDir {
			return c.FS().RemoveAll(remotePath)
		}
		return c.FS().Remove(remotePath)
	},
}

// ---- mkdir ----

var mkdirCmd = &cobra.Command{
	Use:   "mkdir <remote-path>",
	Short: "Create a directory on the remote server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, cfg, c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()
		remotePath := resolveRemotePath(args[0], cfg.RemotePath)
		return c.FS().MkdirAll(remotePath, os.FileMode(cfg.DirPerm))
	},
}

// ---- config ----

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show the loaded configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath, err := resolveConfigPath()
		if err != nil {
			return err
		}
		cfgs, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		fmt.Printf("config file: %s\n", cfgPath)
		for i, c := range cfgs {
			name := c.Name
			if name == "" {
				name = fmt.Sprintf("[%d]", i)
			}
			fmt.Printf("  profile %s: %s://%s@%s:%d%s\n",
				name, c.Protocol, c.Username, c.Host, c.Port, c.RemotePath)
		}
		return nil
	},
}

// ---- shared helpers ----

func connect() (string, *config.Config, client.RemoteClient, error) {
	cfgPath, err := resolveConfigPath()
	if err != nil {
		return "", nil, nil, err
	}
	cfgs, err := config.Load(cfgPath)
	if err != nil {
		return "", nil, nil, err
	}

	cfg, err := selectProfile(cfgs, flagProfile)
	if err != nil {
		return "", nil, nil, err
	}

	workspaceRoot, err := workspaceRootFor(cfgPath)
	if err != nil {
		return "", nil, nil, err
	}

	c, err := client.New(cfg)
	if err != nil {
		return "", nil, nil, err
	}
	fmt.Printf("connecting to %s:%d…\n", cfg.Host, cfg.Port)
	if err := c.Connect(); err != nil {
		return "", nil, nil, fmt.Errorf("connect: %w", err)
	}
	return workspaceRoot, cfg, c, nil
}

// workspaceRootFor returns the directory the user considers the project root:
// the parent of `.vscode/` when the config was auto-discovered, otherwise cwd.
func workspaceRootFor(cfgPath string) (string, error) {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(abs)
	if filepath.Base(parent) == ".vscode" {
		return filepath.Dir(parent), nil
	}
	return os.Getwd()
}

func resolveConfigPath() (string, error) {
	if flagConfig != "" {
		return flagConfig, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return config.FindConfigFile(cwd)
}

func selectProfile(cfgs []config.Config, name string) (*config.Config, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("no profiles in config")
	}
	if name == "" {
		return &cfgs[0], nil
	}
	for i := range cfgs {
		if cfgs[i].Name == name {
			return &cfgs[i], nil
		}
	}
	return nil, fmt.Errorf("profile %q not found", name)
}

func resolveLocalPath(args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}
	return os.Getwd()
}

func makeOpts(cfg *config.Config, dir transfer.Direction) transfer.Options {
	return transfer.Options{
		Direction:   dir,
		Concurrency: cfg.Concurrency,
		FilePerm:    os.FileMode(cfg.FilePerm),
		DirPerm:     os.FileMode(cfg.DirPerm),
		UseTempFile: cfg.UseTempFile,
		Ignore:      cfg.Ignore,
	}
}

func progressPrinter() func(local, remote string, size int64) {
	return func(local, remote string, size int64) {
		fmt.Printf("  ✓ %s (%d bytes)\n", remote, size)
	}
}

func localToRemote(localPath, localBase, remoteBase string) string {
	rel, err := filepath.Rel(localBase, localPath)
	if err != nil {
		return remoteBase
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return remoteBase
	}
	return strings.TrimRight(remoteBase, "/") + "/" + rel
}

// resolveRemotePath joins a user-supplied remote argument with cfg.RemotePath
// when it is not already absolute. Without this, the SFTP server resolves bare
// names against the SSH session's default working directory (usually $HOME),
// not the project's configured remote root.
func resolveRemotePath(arg, remoteBase string) string {
	arg = filepath.ToSlash(arg)
	if strings.HasPrefix(arg, "/") {
		return arg
	}
	base := strings.TrimRight(remoteBase, "/")
	if arg == "" || arg == "." {
		return base
	}
	return base + "/" + strings.TrimPrefix(arg, "./")
}

// defaultLocalDestination picks where a downloaded file/dir should land when
// the user didn't pass an explicit local-path. If the resolved remote sits
// under cfg.RemotePath and we have a workspace root, mirror the relative
// layout into the workspace; otherwise drop the basename into cwd.
func defaultLocalDestination(remotePath, remoteBase, workspaceRoot string) string {
	base := strings.TrimRight(remoteBase, "/")
	if workspaceRoot != "" && strings.HasPrefix(remotePath, base+"/") {
		rel := strings.TrimPrefix(remotePath, base+"/")
		return filepath.Join(workspaceRoot, filepath.FromSlash(rel))
	}
	if workspaceRoot != "" && remotePath == base {
		return workspaceRoot
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, filepath.Base(remotePath))
}

func init() {
	syncCmd.Flags().StringVarP(&syncDirection, "direction", "d", "local-to-remote",
		"sync direction: local-to-remote, remote-to-local, both")
	syncCmd.Flags().BoolVar(&syncDelete, "delete", false,
		"delete extraneous files from destination")
}
