package local

import (
	"context"
	"errors"
	"github.com/tianwei/diskless/internal/storage"
	"strings"
	"testing"
	"time"
)

func TestDeleteAsideImageNeverTreatsAnotherImageCloneAsAConfig(t *testing.T) {
	z := newFakePool()
	root := "tank/nd-rebuilding-123"
	z.datasets = []string{root, root + "/image", root + "/image_default", root + "/saved-image"}
	z.origin[root+"/image_default"] = root + "/image@0"
	z.origin[root+"/saved-image"] = root + "/image@0"
	a := New("node", z, &fakeExporter{})
	_ = a.DeleteAsideImage(context.Background(), "image", []string{"image_default"})
	root = "tank/nd-diverged-123"
	if !z.present(root+"/saved-image") || !z.present(root+"/image") {
		t.Fatalf("derived image or its required source lost: %v", z.destroyed)
	}
}

func TestDeleteAsideImageDeletesExplicitHistoricalObjectsButKeepsSharedHeads(t *testing.T) {
	for _, kind := range []string{"diverged", "rebuilding"} {
		t.Run(kind, func(t *testing.T) {
			z := newFakePool()
			root := "tank/nd-" + kind + "-123"
			z.datasets = []string{root, root + "/db", root + "/files", root + "/image", root + "/image_default"}
			z.snaps[root+"/image@unique-point"] = true
			z.origin[root+"/image_default"] = root + "/image@unique-point"
			a := New("node", z, &fakeExporter{})
			if err := a.DeleteAsideImage(context.Background(), "image", []string{"image_default"}); err != nil {
				t.Fatal(err)
			}
			if !z.present(root) || !z.present(root+"/db") || !z.present(root+"/files") {
				t.Fatalf("unverified file/database heads destroyed: %v", z.destroyed)
			}
			if z.present(root+"/image") || z.present(root+"/image_default") {
				t.Fatalf("explicitly deleted historical objects remain: %v", z.datasets)
			}
		})
	}
}

func TestDeleteAsideImageRejectsUnboundedNamesBeforeMutation(t *testing.T) {
	for _, names := range [][]string{{"../image", "config"}, {"image", "../config"}, {"image@point", "config"}, {"files", "config"}, {"image", "run/CLIENT-001122334455"}} {
		z := newFakePool()
		z.datasets = []string{"tank/nd-rebuilding-123"}
		a := New("node", z, &fakeExporter{})
		err := a.DeleteAsideImage(context.Background(), names[0], []string{names[1]})
		if err == nil || len(z.destroyAttempts) > 0 {
			t.Errorf("invalid names accepted: %v err=%v changes=%v", names, err, z.destroyAttempts)
		}
	}
}

func TestDeleteAsideImageReclaimsOnlyExplicitRebuildingObjectsAndCanRetry(t *testing.T) {
	z := newFakePool()
	root := "tank/nd-rebuilding-123"
	z.datasets = []string{root, root + "/db", root + "/files", root + "/image", root + "/image_default"}
	z.origin[root+"/image_default"] = root + "/image@0"
	z.failOn = "destroy " + root + "/image_default"
	a := New("node", z, &fakeExporter{})
	if err := a.DeleteAsideImage(context.Background(), "image", []string{"image_default"}); err == nil {
		t.Fatal("failed deletion must remain retryable")
	}
	if err := a.DeleteAsideImage(context.Background(), "image", []string{"image_default"}); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{root + "/image", root + "/image_default"} {
		if z.present(gone) {
			t.Fatalf("explicit old object left behind: %s", gone)
		}
	}
	for _, kept := range []string{root, root + "/db", root + "/files"} {
		if !z.present(kept) {
			t.Fatalf("unverified head removed: %s", kept)
		}
	}
}

func TestDeleteAsideImageRetainsDerivedImageAndOnlyItsRequiredBaselines(t *testing.T) {
	for _, kind := range []string{"diverged", "rebuilding"} {
		t.Run(kind, func(t *testing.T) {
			z := newFakePool()
			root := "tank/nd-" + kind + "-42"
			z.datasets = []string{root, root + "/image", root + "/image_default", root + "/saved-image", root + "/other-config"}
			z.origin[root+"/image_default"] = root + "/image@0"
			z.origin[root+"/saved-image"] = root + "/image_default@new-image"
			z.origin[root+"/other-config"] = root + "/image@0"
			a := New("node", z, &fakeExporter{})
			if err := a.DeleteAsideImage(context.Background(), "image", []string{"image_default", "other-config"}); err != nil {
				t.Fatal(err)
			}
			if kind == "rebuilding" {
				root = "tank/nd-diverged-42"
			}
			for _, kept := range []string{root + "/image", root + "/image_default", root + "/saved-image"} {
				if !z.present(kept) {
					t.Fatalf("derived image baseline lost: %s", kept)
				}
			}
			if z.present(root + "/other-config") {
				t.Fatal("unneeded explicitly deleted configuration retained")
			}
		})
	}
}

func TestDeleteAsideImageRefusesLiveClientDependenciesRatherThanTreatingThemAsNewImages(t *testing.T) {
	z := newFakePool()
	root := "tank/nd-diverged-123"
	client := "tank/run/CLIENT-001122334455"
	z.datasets = []string{root, root + "/image", root + "/cfg", client}
	z.origin[root+"/cfg"] = root + "/image@0"
	z.origin[client] = root + "/cfg@1"
	a := New("node", z, &fakeExporter{})
	err := a.DeleteAsideImage(context.Background(), "image", []string{"cfg"})
	if err == nil || !strings.Contains(err.Error(), "001122334455") {
		t.Fatalf("live-client dependency not reported: %v", err)
	}
	if len(z.destroyAttempts) > 0 {
		t.Fatalf("live dependency touched: %v", z.destroyAttempts)
	}
}

func TestDeleteAsideImageMovesProtectedRebuildingToDiverged(t *testing.T) {
	z := newFakePool()
	old, kept := "tank/nd-rebuilding-42", "tank/nd-diverged-42"
	z.datasets = []string{old, old + "/image", old + "/cfg", old + "/saved-image", old + "/delete-config"}
	z.origin[old+"/cfg"] = old + "/image@0"
	z.origin[old+"/saved-image"] = old + "/cfg@new"
	z.snaps[old+"/saved-image@0"] = true
	a := New("node", z, &fakeExporter{})
	if err := a.DeleteAsideImage(context.Background(), "image", []string{"cfg", "delete-config"}); err != nil {
		t.Fatal(err)
	}
	if z.present(old) || !z.present(kept) {
		t.Fatalf("saved image is still eligible for automatic rebuilding prune: %v", z.datasets)
	}
	for _, ds := range []string{"image", "cfg", "saved-image"} {
		if !z.present(kept + "/" + ds) {
			t.Fatalf("protected data not preserved: %s %v", ds, z.datasets)
		}
	}
	if z.present(kept + "/delete-config") {
		t.Fatal("unrelated authorized config was not deleted")
	}
	if z.origin[kept+"/saved-image"] != kept+"/cfg@new" {
		t.Fatalf("clone origin did not follow root rename: %v", z.origin)
	}
}

func TestDeleteAsideImageCannotReportSuccessWhenPreservationRenameConflicts(t *testing.T) {
	z := newFakePool()
	old := "tank/nd-rebuilding-42"
	z.datasets = []string{old, old + "/image", old + "/saved-image", "tank/nd-diverged-42"}
	z.origin[old+"/saved-image"] = old + "/image@0"
	z.snaps[old+"/saved-image@0"] = true
	a := New("node", z, &fakeExporter{})
	if err := a.DeleteAsideImage(context.Background(), "image", nil); err == nil {
		t.Fatal("rename collision must block metadata deletion and allow retry")
	}
	if !z.present(old+"/saved-image") || !z.present("tank/nd-diverged-42") {
		t.Fatal("preservation collision overwrote content")
	}
}

func TestDeleteAsideImageUsesTheSameLockAsWholeTreePruning(t *testing.T) {
	release, err := storage.HoldAsideCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	z := newFakePool()
	z.datasets = []string{"tank/nd-rebuilding-42", "tank/nd-rebuilding-42/image"}
	a := New("node", z, &fakeExporter{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.DeleteAsideImage(ctx, "image", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("explicit cleanup bypassed prune lock: %v", err)
	}
	if len(z.destroyAttempts) > 0 {
		t.Fatalf("mutated under automatic prune lock: %v", z.destroyAttempts)
	}
}
