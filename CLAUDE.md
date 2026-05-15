# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build      # builds to build/sftp-lsp
make install    # go install to $GOPATH/bin
make test       # go test ./...
make lint       # golangci-lint run ./...
make tidy       # go mod tidy

go test ./internal/transfer/...   # run a single package's tests
```

## Architecture

`sftp-lsp` is a single Go binary with two modes selected at startup:

- **Default (no `--cli`)**: LSP server over stdin/stdout using JSON-RPC 2.0, intended to be launched by an editor (VS Code, Neovim, etc.)
- **`--cli` flag**: Cobra CLI for scripted use (`upload`, `download`, `sync`, `list`, `delete`, `mkdir`, `watch`, `config`)

### Package layout

| Package | Role |
|---|---|
| `internal/lsp` | JSON-RPC 2.0 transport (`server.go`), LSP protocol types (`protocol.go`), and the SFTP command handler (`handler.go`) |
| `internal/config` | Loads `.vscode/sftp.json` (single object or array of profiles); walks up directories to find it |
| `internal/client` | `RemoteClient` interface + SFTP (`sftp.go`) and FTP (`ftp.go`) backends |
| `internal/fs` | `FileSystem` interface — the shared abstraction used by both local (`local.go`) and remote backends |
| `internal/transfer` | `Engine` (file/directory copy between two `FileSystem`s), `Sync` (bi-directional sync with delete/update/ignore), `Scheduler` (bounded-concurrency goroutine pool) |
| `internal/cli` | Cobra subcommands; `watch.go` uses `fsnotify` with 200 ms debounce |

### Key data flow (LSP mode)

1. Editor sends `initialize` → handler reads `.vscode/sftp.json` from `rootUri`
2. `workspace/executeCommand` with an `sftp.*` command → `SFTPHandler.runCommand`
3. `runCommand` calls `getOrConnectClient` (lazy-connects, caches by profile name/host), builds a `transfer.Engine`, and dispatches to upload/download/sync helpers
4. `textDocument/didSave` triggers `sftp.upload.file` when `uploadOnSave: true`
5. Progress is reported via `window/workDoneProgress/create` + `$/progress` notifications

### `fs.FileSystem` interface

This is the core abstraction. Everything in `transfer` is written against it. Local disk (`fs/local.go`), SFTP (`client/sftp.go`), and FTP (`client/ftp.go`) all implement it. Adding a new backend means implementing this interface and registering it in `client/client.go`.

### Config (`sftp.json`)

Compatible with the VS Code SFTP extension schema. Discovered by walking up from the workspace root looking for `.vscode/sftp.json`. Supports multiple profiles (array). `protocol` defaults to `sftp`; ports default to 22/21; concurrency defaults to 4.

### LSP commands exposed

All `sftp.*` commands are listed in `handler.go:supportedCommands`. Arguments are passed as a single JSON object (`SftpCommandArgs`) with `localPath`, `remotePath`, and `configName`.
