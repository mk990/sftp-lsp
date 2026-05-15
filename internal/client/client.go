package client

import (
	"fmt"

	"sftp-lsp/internal/config"
	"sftp-lsp/internal/fs"
)

// RemoteClient manages a single connection to a remote server.
type RemoteClient interface {
	// Connect establishes the connection.
	Connect() error
	// Close tears down the connection.
	Close() error
	// FS returns the FileSystem view of the remote server.
	// Only valid after a successful Connect call.
	FS() fs.FileSystem
}

// New creates the appropriate RemoteClient for the given config.
func New(cfg *config.Config) (RemoteClient, error) {
	switch cfg.Protocol {
	case config.ProtocolSFTP, "":
		return NewSFTPClient(cfg), nil
	case config.ProtocolFTP:
		return NewFTPClient(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", cfg.Protocol)
	}
}
