package main

import (
	"fmt"
	"os"

	"sftp-lsp/internal/cli"
	"sftp-lsp/internal/lsp"
)

var version = "dev" // overridden by -ldflags at build time

func main() {
	// Strip mode-selecting flags from anywhere in os.Args so they work no matter
	// where they appear — including after `__complete`, which is how cobra's
	// generated shell-completion scripts invoke us (e.g. `sftp-lsp __complete
	// --cli upload ""`). `--stdio`/`-stdio` is an editor-friendly alias for the
	// default LSP mode (gopls / typescript-language-server convention).
	cliMode := false
	filtered := os.Args[:1]
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--cli":
			cliMode = true
			continue
		case "--stdio", "-stdio":
			continue
		}
		filtered = append(filtered, arg)
	}
	os.Args = filtered

	if cliMode {
		cli.Execute()
		return
	}

	// Cobra's completion subcommands — including the hidden `__complete` helpers
	// the generated shell scripts call into — route to CLI without requiring
	// `--cli`, so `source <(sftp-lsp completion zsh)` works and tab-completion
	// at the shell doesn't get stuck in LSP mode.
	for _, arg := range os.Args[1:] {
		if isCompletionCommand(arg) {
			cli.Execute()
			return
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

func isCompletionCommand(s string) bool {
	switch s {
	case "completion", "__complete", "__completeNoDesc":
		return true
	}
	return false
}
