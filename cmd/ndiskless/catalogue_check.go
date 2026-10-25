package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/tianwei/diskless/internal/control/ops"
	"github.com/tianwei/diskless/internal/storage"
	"github.com/tianwei/diskless/internal/store"
)

// dbCopyMissing opens the replicated database copy from a temporary file and identifies records whose datasets are missing from the pool.
// On inconclusive checks, report nothing missing: PrepareActiveDB rejects absent copies and pool health checks catch unreadable storage.
func dbCopyMissing(ctx context.Context, copyPath string, agent storage.StorageAgent, logger *slog.Logger) []string {
	data, err := os.ReadFile(copyPath)
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "ndiskless-dbcheck-")
	if err != nil {
		logger.Warn("catalogue completeness check skipped", "error", err)
		return nil
	}
	defer os.RemoveAll(dir)
	path := dir + "/ndiskless.db"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		logger.Warn("catalogue completeness check skipped", "error", err)
		return nil
	}
	st, err := store.Open(ctx, "file:"+path)
	if err != nil {
		logger.Warn("catalogue completeness check skipped", "error", err)
		return nil
	}
	defer st.Close()
	missing, err := ops.ReconcileService{Store: st, Storage: agent}.MissingDatasets(ctx)
	if err != nil {
		logger.Warn("catalogue completeness check skipped", "error", err)
		return nil
	}
	return missing
}
