package main

import "context"

// standbyCleaner is the minimal storage-agent interface required by quiesceStandby, allowing tests to verify safe operation ordering.
type standbyCleaner interface {
	ClientCloneMACs(ctx context.Context) ([]string, error)
	CleanupClientClones(ctx context.Context, mac string, keep []string) error
	SuperCloneMACs(ctx context.Context) ([]string, error)
	CleanupSuperClones(ctx context.Context, mac string) error
}

// quiesceStandby removes standby-owned resources: first unexport iSCSI targets, then clean up clones.
// Ordering is essential: once exports are removed, this node cannot serve active client volumes, even if session detection is unreliable.
// Previous cleanup logic deleted in-use volumes because it could not see CHAP sessions.
// Also clean Super Admin clones: after failover, old-writer clones might otherwise remain and be reused
// depending on VIP ownership. Unsaved Super Admin sessions are not guaranteed to survive failover.
// Cleanup is best-effort: an incomplete cleanup should not turn an otherwise valid standby startup into a failure.
func quiesceStandby(ctx context.Context, teardownExports func(context.Context) error,
	cleaner standbyCleaner, logf func(msg string, args ...any)) {
	if teardownExports != nil {
		if err := teardownExports(ctx); err != nil {
			logf("standby quiesce: LIO teardown failed", "error", err)
		}
	}
	if cleaner == nil {
		return
	}
	if macs, err := cleaner.ClientCloneMACs(ctx); err != nil {
		logf("standby quiesce: client clone listing failed", "error", err)
	} else {
		for _, mac := range macs {
			if err := cleaner.CleanupClientClones(ctx, mac, nil); err != nil {
				logf("standby quiesce: clone cleanup failed", "mac", mac, "error", err)
			}
		}
	}
	if macs, err := cleaner.SuperCloneMACs(ctx); err != nil {
		logf("standby quiesce: super clone listing failed", "error", err)
	} else {
		for _, mac := range macs {
			if err := cleaner.CleanupSuperClones(ctx, mac); err != nil {
				logf("standby quiesce: super clone cleanup failed", "mac", mac, "error", err)
			}
		}
	}
}
