package lsp

import (
	"context"
	"time"
)

const (
	diagnosticsDeadline     = 5 * time.Second
	diagnosticsPollInterval = 100 * time.Millisecond
)

// WaitForDiagnostics waits up to 5s (or until ctx is done) for any LSP client
// to provide diagnostics for the given absolute file path. It proactively
// opens or notifies change for the file to trigger diagnostics.
func WaitForDiagnostics(ctx context.Context, filePath string, clients map[string]*Client) {
	if len(clients) == 0 || filePath == "" {
		return
	}

	for _, client := range clients {
		if client.IsFileOpen(filePath) {
			_ = client.NotifyChange(ctx, filePath)
		} else {
			_ = client.OpenFile(ctx, filePath)
		}
	}

	deadline := time.Now().Add(diagnosticsDeadline)
	for time.Now().Before(deadline) {
		for _, client := range clients {
			current := client.GetDiagnostics()
			for uri := range current {
				path, err := uri.Path()
				if err != nil {
					continue
				}
				if path == filePath {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(diagnosticsPollInterval):
		}
	}
}
