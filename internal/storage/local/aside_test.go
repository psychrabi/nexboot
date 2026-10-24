package local

import (
	"context"
	"testing"
)

// Explicit historical objects may be reclaimed; unknown derived images and their required sources remain.
func TestDeleteAsideImagePreservesUnlistedObjectsAndTheirBaselines(t *testing.T) {
	z := newFakePool()
	d, r := "tank/nd-diverged-1", "tank/nd-rebuilding-2"
	z.datasets = []string{
		"tank/nd/keep",
		d, d + "/db", d + "/files", d + "/win10", d + "/win10_default", d + "/win10_fork",
		r, r + "/db", r + "/win10", r + "/win10_default", r + "/saved",
		"tank/run/IDENT-000000000001",
	}
	z.snaps = map[string]bool{d + "/win10@0": true, d + "/win10_default@1": true, r + "/win10@0": true, r + "/win10_default@1": true}
	z.origin = map[string]string{
		d + "/win10_default":          d + "/win10@0",
		d + "/win10_fork":             d + "/win10@0", // 库里没记的派生配置，也是这个镜像的
		r + "/win10_default":          r + "/win10@0",
		r + "/saved":                  r + "/win10_default@1", // 由这个镜像的配置另存出的新镜像
		"tank/run/IDENT-000000000001": d + "/win10_default@1",
	}
	a := New("node-a", z, &fakeExporter{})

	_ = a.DeleteAsideImage(context.Background(), "win10", []string{"win10_default"})
	r = "tank/nd-diverged-2"
	for _, kept := range []string{d, d + "/win10", d + "/win10_fork", r, r + "/win10", r + "/win10_default", r + "/saved"} {
		if !z.present(kept) {
			t.Fatalf("%s must remain: %v", kept, z.destroyed)
		}
	}
	for _, gone := range []string{d + "/win10_default", "tank/run/IDENT-000000000001"} {
		if z.present(gone) {
			t.Fatalf("explicit historical object remains: %s", gone)
		}
	}

}
