package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sftp-lsp/internal/client"
	"sftp-lsp/internal/config"
	"sftp-lsp/internal/fs"
	"sftp-lsp/internal/transfer"
)

// Handler processes LSP messages.
type Handler interface {
	Handle(srv *Server, msg *Message) (interface{}, error)
}

// SFTPHandler implements the LSP Handler for all SFTP operations.
type SFTPHandler struct {
	mu          sync.Mutex
	configs     []config.Config   // loaded from sftp.json
	clients     map[string]client.RemoteClient // keyed by config.Name
	rootURI     string
	initialized bool
}

func NewSFTPHandler() *SFTPHandler {
	return &SFTPHandler{clients: make(map[string]client.RemoteClient)}
}

var supportedCommands = []string{
	"sftp.upload.file",
	"sftp.upload.folder",
	"sftp.upload.activeFile",
	"sftp.upload.project",
	"sftp.download.file",
	"sftp.download.folder",
	"sftp.download.activeFile",
	"sftp.download.project",
	"sftp.sync.localToRemote",
	"sftp.sync.remoteToLocal",
	"sftp.sync.bothDirections",
	"sftp.list",
	"sftp.delete.remote",
	"sftp.create.folder",
	"sftp.cancelAllTransfer",
	"sftp.diff",
}

func (h *SFTPHandler) Handle(srv *Server, msg *Message) (interface{}, error) {
	switch msg.Method {
	case "initialize":
		return h.handleInitialize(srv, msg)
	case "initialized":
		return nil, nil
	case "shutdown":
		h.closeAll()
		return nil, nil
	case "exit":
		os.Exit(0)
		return nil, nil
	case "workspace/executeCommand":
		return h.handleExecuteCommand(srv, msg)
	case "textDocument/didSave":
		go h.handleDidSave(srv, msg)
		return nil, nil
	default:
		return nil, &ResponseError{Code: MethodNotFound, Message: fmt.Sprintf("method not found: %s", msg.Method)}
	}
}

func (h *SFTPHandler) handleInitialize(srv *Server, msg *Message) (*InitializeResult, error) {
	var params InitializeParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return nil, err
	}

	h.rootURI = params.RootURI
	rootPath := uriToPath(params.RootURI)
	if rootPath == "" {
		rootPath = params.RootPath
	}

	// Attempt to load sftp.json from the workspace root.
	if rootPath != "" {
		cfgFile, err := config.FindConfigFile(rootPath)
		if err == nil {
			cfgs, err := config.Load(cfgFile)
			if err == nil {
				h.configs = cfgs
			} else {
				srv.ShowMessage(MessageWarning, fmt.Sprintf("sftp-lsp: config error: %v", err))
			}
		}
	}

	h.initialized = true

	return &InitializeResult{
		ServerInfo: ServerInfo{Name: "sftp-lsp", Version: "0.1.0"},
		Capabilities: ServerCapabilities{
			TextDocumentSync: &TextDocumentSyncOptions{
				OpenClose: false,
				Save:      &SaveOptions{IncludeText: false},
			},
			ExecuteCommandProvider: &ExecuteCommandOptions{
				Commands: supportedCommands,
			},
			WorkspaceProvider: &WorkspaceCapabilities{
				WorkspaceFolders: &WorkspaceFoldersCapability{
					Supported:           true,
					ChangeNotifications: true,
				},
			},
		},
	}, nil
}

func (h *SFTPHandler) handleExecuteCommand(srv *Server, msg *Message) (interface{}, error) {
	var params ExecuteCommandParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return nil, err
	}

	args, err := parseArgs(params.Arguments)
	if err != nil {
		return nil, err
	}

	cfg, err := h.resolveConfig(args.ConfigName)
	if err != nil {
		return nil, err
	}

	// Create progress token.
	token := fmt.Sprintf("sftp-%d", time.Now().UnixNano())
	srv.SendRequest("window/workDoneProgress/create", WorkDoneProgressCreateParams{Token: token})
	srv.ReportProgress(token, WorkDoneProgressBegin{
		Kind:  "begin",
		Title: commandTitle(params.Command),
	})

	go func() {
		err := h.runCommand(params.Command, args, cfg)
		if err != nil {
			srv.ShowMessage(MessageError, fmt.Sprintf("sftp-lsp: %v", err))
			srv.ReportProgress(token, WorkDoneProgressEnd{Kind: "end", Message: "failed: " + err.Error()})
		} else {
			srv.ReportProgress(token, WorkDoneProgressEnd{Kind: "end", Message: "done"})
		}
	}()

	return nil, nil
}

func (h *SFTPHandler) runCommand(command string, args SftpCommandArgs, cfg *config.Config) error {
	c, err := h.getOrConnectClient(cfg)
	if err != nil {
		return err
	}

	localFS := fs.NewLocal()
	engine := transfer.NewEngine(localFS, c.FS(), args.LocalPath, cfg.RemotePath)

	opts := transfer.Options{
		Concurrency: cfg.Concurrency,
		FilePerm:    os.FileMode(cfg.FilePerm),
		DirPerm:     os.FileMode(cfg.DirPerm),
		UseTempFile: cfg.UseTempFile,
		Ignore:      cfg.Ignore,
	}

	ctx := context.Background()

	switch command {
	case "sftp.upload.file", "sftp.upload.activeFile":
		opts.Direction = transfer.Upload
		remotePath := localToRemotePath(args.LocalPath, args.LocalPath, cfg.RemotePath)
		return engine.UploadFile(ctx, args.LocalPath, remotePath, opts)

	case "sftp.upload.folder", "sftp.upload.activeFolder":
		opts.Direction = transfer.Upload
		remotePath := localToRemotePath(args.LocalPath, args.LocalPath, cfg.RemotePath)
		return engine.Transfer(ctx, args.LocalPath, remotePath, opts)

	case "sftp.upload.project":
		opts.Direction = transfer.Upload
		return engine.Transfer(ctx, args.LocalPath, cfg.RemotePath, opts)

	case "sftp.download.file", "sftp.download.activeFile":
		opts.Direction = transfer.Download
		localPath := remoteToLocalPath(args.RemotePath, cfg.RemotePath, args.LocalPath)
		return engine.DownloadFile(ctx, args.RemotePath, localPath, opts)

	case "sftp.download.folder", "sftp.download.activeFolder":
		opts.Direction = transfer.Download
		localPath := remoteToLocalPath(args.RemotePath, cfg.RemotePath, args.LocalPath)
		return engine.Transfer(ctx, localPath, args.RemotePath, opts)

	case "sftp.download.project":
		opts.Direction = transfer.Download
		return engine.Transfer(ctx, args.LocalPath, cfg.RemotePath, opts)

	case "sftp.sync.localToRemote":
		syncOpts := buildSyncOpts(cfg, opts)
		syncOpts.Direction = transfer.SyncLocalToRemote
		_, err := transfer.Sync(ctx, engine, args.LocalPath, cfg.RemotePath, syncOpts)
		return err

	case "sftp.sync.remoteToLocal":
		syncOpts := buildSyncOpts(cfg, opts)
		syncOpts.Direction = transfer.SyncRemoteToLocal
		_, err := transfer.Sync(ctx, engine, args.LocalPath, cfg.RemotePath, syncOpts)
		return err

	case "sftp.sync.bothDirections":
		syncOpts := buildSyncOpts(cfg, opts)
		syncOpts.Direction = transfer.SyncBothDirections
		_, err := transfer.Sync(ctx, engine, args.LocalPath, cfg.RemotePath, syncOpts)
		return err

	case "sftp.list":
		listPath := args.RemotePath
		if listPath == "" {
			listPath = cfg.RemotePath
		}
		entries, err := c.FS().List(listPath)
		if err != nil {
			return err
		}
		_ = entries // result returned async — send as notification
		return nil

	case "sftp.delete.remote":
		fi, err := c.FS().Stat(args.RemotePath)
		if err != nil {
			return err
		}
		if fi.IsDir {
			return c.FS().RemoveAll(args.RemotePath)
		}
		return c.FS().Remove(args.RemotePath)

	case "sftp.create.folder":
		return c.FS().MkdirAll(args.RemotePath, os.FileMode(cfg.DirPerm))

	case "sftp.cancelAllTransfer":
		// Future: cancel running context.
		return nil

	default:
		return fmt.Errorf("unknown command: %s", command)
	}
}

func (h *SFTPHandler) handleDidSave(srv *Server, msg *Message) {
	var params DidSaveTextDocumentParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return
	}

	localPath := uriToPath(params.TextDocument.URI)
	if localPath == "" {
		return
	}

	cfg, err := h.resolveConfig("")
	if err != nil || !cfg.UploadOnSave {
		return
	}

	// Check ignore patterns.
	for _, pattern := range cfg.Ignore {
		if matched, _ := filepath.Match(pattern, filepath.Base(localPath)); matched {
			return
		}
	}

	args := SftpCommandArgs{LocalPath: localPath}
	if err := h.runCommand("sftp.upload.file", args, cfg); err != nil {
		srv.ShowMessage(MessageError, fmt.Sprintf("upload on save: %v", err))
	}
}

// getOrConnectClient returns a connected client for the given config, creating
// one if it doesn't exist yet.
func (h *SFTPHandler) getOrConnectClient(cfg *config.Config) (client.RemoteClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := cfg.Name
	if key == "" {
		key = cfg.Host
	}

	if c, ok := h.clients[key]; ok {
		return c, nil
	}

	c, err := client.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := c.Connect(); err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.Host, err)
	}
	h.clients[key] = c
	return c, nil
}

func (h *SFTPHandler) resolveConfig(name string) (*config.Config, error) {
	if len(h.configs) == 0 {
		return nil, fmt.Errorf("no sftp.json config loaded")
	}
	if name == "" {
		return &h.configs[0], nil
	}
	for i := range h.configs {
		if h.configs[i].Name == name {
			return &h.configs[i], nil
		}
	}
	return nil, fmt.Errorf("profile %q not found", name)
}

func (h *SFTPHandler) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		_ = c.Close()
	}
	h.clients = make(map[string]client.RemoteClient)
}

// ---- helpers ----

func parseArgs(raw []interface{}) (SftpCommandArgs, error) {
	var args SftpCommandArgs
	if len(raw) == 0 {
		return args, nil
	}
	b, err := json.Marshal(raw[0])
	if err != nil {
		return args, err
	}
	err = json.Unmarshal(b, &args)
	return args, err
}

func buildSyncOpts(cfg *config.Config, base transfer.Options) transfer.SyncOptions {
	return transfer.SyncOptions{
		Concurrency:      base.Concurrency,
		Delete:           cfg.SyncOption.Delete,
		SkipCreate:       cfg.SyncOption.SkipCreate,
		IgnoreExisting:   cfg.SyncOption.IgnoreExisting,
		Update:           cfg.SyncOption.Update,
		FilePerm:         os.FileMode(cfg.FilePerm),
		DirPerm:          os.FileMode(cfg.DirPerm),
		Ignore:           cfg.Ignore,
		RemoteTimeOffset: time.Duration(float64(time.Hour) * cfg.RemoteTimeOffsetHours),
	}
}

func localToRemotePath(localPath, localBase, remoteBase string) string {
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

func remoteToLocalPath(remotePath, remoteBase, localBase string) string {
	if !strings.HasPrefix(remotePath, remoteBase) {
		return localBase
	}
	rel := strings.TrimPrefix(remotePath, remoteBase)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return localBase
	}
	return filepath.Join(localBase, filepath.FromSlash(rel))
}

func uriToPath(uri string) string {
	const prefix = "file://"
	if !strings.HasPrefix(uri, prefix) {
		return uri
	}
	return uri[len(prefix):]
}

func commandTitle(cmd string) string {
	m := map[string]string{
		"sftp.upload.file":          "Upload File",
		"sftp.upload.folder":        "Upload Folder",
		"sftp.upload.activeFile":    "Upload Active File",
		"sftp.upload.project":       "Upload Project",
		"sftp.download.file":        "Download File",
		"sftp.download.folder":      "Download Folder",
		"sftp.download.activeFile":  "Download Active File",
		"sftp.download.project":     "Download Project",
		"sftp.sync.localToRemote":   "Sync Local → Remote",
		"sftp.sync.remoteToLocal":   "Sync Remote → Local",
		"sftp.sync.bothDirections":  "Sync Both Directions",
		"sftp.list":                 "List Remote",
		"sftp.delete.remote":        "Delete Remote",
		"sftp.create.folder":        "Create Remote Folder",
	}
	if t, ok := m[cmd]; ok {
		return t
	}
	return cmd
}
