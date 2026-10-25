package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Catch-up may require multiple pulls after a rejected recursive transfer; retry until the target revision is fully present.
func TestCatchUpToPullsUntilTheTargetRoundIsHeld(t *testing.T) {
	held := "rep-100"
	pulls := 0
	err := catchUpTo(context.Background(), "rep-300",
		func(context.Context) error {
			pulls++
			if pulls == 3 {
				held = "rep-300"
			}
			return nil
		},
		func(context.Context) string { return held }, time.Millisecond)
	if err != nil || pulls != 3 {
		t.Fatalf("err=%v pulls=%d", err, pulls)
	}
}

// A revision newer than target is also caught up because additional changes may arrive after sampling.
func TestCatchUpToAcceptsANewerRound(t *testing.T) {
	err := catchUpTo(context.Background(), "rep-300",
		func(context.Context) error { return nil },
		func(context.Context) string { return "rep-400" }, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
}

// On deadline, preserve the local revision and last failure for takeover diagnostics.
func TestCatchUpToGivesUpAtTheDeadlineAndSaysWhy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := catchUpTo(ctx, "rep-300",
		func(context.Context) error {
			return errors.New("Peer http://192.168.10.3:8080 returned 500: pool is suspended")
		},
		func(context.Context) string { return "rep-100" }, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "rep-300") || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("err=%v", err)
	}
}
