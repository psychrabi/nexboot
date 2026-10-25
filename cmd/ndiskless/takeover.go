package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tianwei/diskless/internal/control/ops"
)

// catchUpTo repeatedly pulls until the local node fully holds target or ctx expires. One pull may be insufficient when recursive transfer falls back to individual datasets,
// or new changes arrive between pulls.
func catchUpTo(ctx context.Context, target string, pull func(context.Context) error,
	held func(context.Context) string, pause time.Duration) error {
	for {
		err := pull(ctx)
		now := held(ctx)
		if ops.CaughtUpTo(now, target) {
			return nil
		}
		select {
		case <-ctx.Done():
			if now == "" {
				now = "none"
			}
			if err != nil {
				return fmt.Errorf("Failed to catch up to %s before deadline (local revision %s): %w", target, now, err)
			}
			return fmt.Errorf("Failed to catch up to %s before deadline (local revision %s)", target, now)
		case <-time.After(pause):
		}
	}
}
