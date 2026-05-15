# sftp-lsp

A single Go binary that ships **two modes**:

- **LSP server** over stdio (default) — drop-in for editors that want SFTP/FTP operations as workspace commands.
- **CLI** (`--cli`) — Cobra subcommands for scripted use.

Config is read from `.vscode/sftp.json` (VS Code SFTP extension schema), auto-discovered by walking up from the workspace root.

## Build

```bash
make build      # → build/sftp-lsp
make install    # → $GOPATH/bin/sftp-lsp
make test
make lint
```

## LSP mode

Run the binary with no arguments (or with `--stdio`). It speaks JSON-RPC 2.0 over stdin/stdout.

```json
{ "command": "sftp-lsp" }
// or, following the gopls / typescript-language-server convention:
{ "command": "sftp-lsp", "args": ["--stdio"] }
```

`--stdio` is a no-op alias for the default mode; both invocations are equivalent. Once the editor sends `initialize`, the workspace's `.vscode/sftp.json` is loaded automatically.

### Supported `workspace/executeCommand` commands

| Command | Args (single JSON object) |
|---|---|
| `sftp.upload.file` / `.activeFile` | `localPath` |
| `sftp.upload.folder` / `.project` | `localPath` |
| `sftp.download.file` / `.folder` / `.project` | `remotePath`, `localPath` |
| `sftp.sync.localToRemote` / `.remoteToLocal` / `.bothDirections` | `localPath` |
| `sftp.list` | `remotePath` |
| `sftp.delete.remote` | `remotePath` |
| `sftp.create.folder` | `remotePath` |
| `sftp.cancelAllTransfer` | — |

All commands accept an optional `configName` to pick a profile when `sftp.json` contains an array.

Saves trigger `sftp.upload.file` automatically when the active profile has `"uploadOnSave": true`.

## CLI mode

```bash
sftp-lsp --cli <subcommand> [flags]
```

Persistent flags: `--config <path>`, `--profile <name>`, `--dry-run`.

### Upload / sync path semantics

Both `upload` and `sync` compute the remote path **relative to the workspace root** (the directory containing `.vscode/sftp.json`). From a workspace at `/work` with `"remotePath": "/var/www"`:

| Command | Remote target |
|---|---|
| `sftp-lsp --cli upload` | `/var/www` |
| `sftp-lsp --cli upload app` | `/var/www/app` |
| `sftp-lsp --cli upload app/main.go` | `/var/www/app/main.go` |
| `sftp-lsp --cli upload src/components` | `/var/www/src/components` |
| `sftp-lsp --cli sync app -d local-to-remote` | `/var/www/app` ↔ `./app` |

Uploads always overwrite existing remote files (SFTP `O_TRUNC`, FTP `STOR`). Set `"useTempFile": true` in `sftp.json` to write to `*.sftptmp` and rename over the target instead.

### Subcommands

```bash
sftp-lsp --cli upload [local-path]                          # default: cwd
sftp-lsp --cli download <remote-path> [local-path]
sftp-lsp --cli sync [local-path] -d <local-to-remote|remote-to-local|both> [--delete]
sftp-lsp --cli list [remote-path]
sftp-lsp --cli delete <remote-path>
sftp-lsp --cli mkdir <remote-path>
sftp-lsp --cli watch [local-path] [--delete]
sftp-lsp --cli config                                       # print loaded config
```

`watch` uses `fsnotify` with a 200 ms debounce and respects `.gitignore` plus `ignoreFile` patterns.

## Config (`.vscode/sftp.json`)

Compatible with the VS Code SFTP extension. Single object or array of profiles.

```jsonc
{
  "name": "production",
  "host": "example.com",
  "port": 22,
  "protocol": "sftp",            // or "ftp"
  "username": "deploy",
  "privateKeyPath": "~/.ssh/id_ed25519",
  "remotePath": "/var/www",
  "uploadOnSave": false,
  "useTempFile": false,
  "concurrency": 4,
  "ignore": ["node_modules", "*.log", ".git"],
  "syncOption": { "delete": false, "update": false }
}
```

`protocol` defaults to `sftp`; ports default to 22/21; `concurrency` defaults to 4. Values from `~/.ssh/config` (User, Port, Hostname, IdentityFile, ProxyCommand) are overlaid where the JSON leaves them unset.

## Architecture

| Package | Role |
|---|---|
| `internal/lsp` | JSON-RPC 2.0 transport, LSP protocol types, SFTP command handler |
| `internal/config` | Loads `.vscode/sftp.json`; walks up directories to find it |
| `internal/client` | `RemoteClient` interface + SFTP and FTP backends |
| `internal/fs` | `FileSystem` interface shared by local and remote backends |
| `internal/transfer` | `Engine` (file/dir copy), `Sync` (bi-directional), `Scheduler` (bounded concurrency) |
| `internal/cli` | Cobra subcommands; `watch.go` uses `fsnotify` |

Adding a new remote backend means implementing `fs.FileSystem` and registering it in `client/client.go`.

## Releases

Tagged pushes (`v*`) build cross-platform binaries via `.github/workflows/release.yml` (Linux/macOS amd64+arm64, Windows amd64) and attach `.tar.gz` / `.zip` archives plus SHA-256 checksums to a GitHub release.

```bash
git tag v0.1.0
git push --tags
```

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
