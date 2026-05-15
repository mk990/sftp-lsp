package main

import (
	"fmt"
	"os"

	"sftp-lsp/internal/cli"
	"sftp-lsp/internal/lsp"
)

var version = "dev" // overridden by -ldflags at build time

func main() {
	for i, arg := range os.Args[1:] {
		switch arg {
		case "--cli":
			os.Args = append(os.Args[:i+1], os.Args[i+2:]...)
			cli.Execute()
			return
		case "--stdio", "-stdio":
			// Explicit alias for the default LSP-over-stdio mode. Recognized so
			// editor configurations that follow the gopls / typescript-language-server
			// convention work out of the box. Stripped from os.Args for tidiness.
			os.Args = append(os.Args[:i+1], os.Args[i+2:]...)
		}
		if !isFlag(arg) {
			break
		}
	}

	// Default: LSP server over stdio.
	handler := lsp.NewSFTPHandler()
	srv := lsp.NewServer(os.Stdin, os.Stdout, handler)
	if err := srv.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "sftp-lsp:", err)
		os.Exit(1)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }
