package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tianwei/diskless/internal/domain"
)

func TestTerminalIdentityKeepsTemplateBootRecord(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	at := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	in := domain.TerminalIdentity{ID: "t", TerminalID: "t", ProfileID: "p", InstanceID: "PCI\\VEN_8086&DEV_10D3\\X", Source: "computed",
		TemplateBootRef: "r1", TemplateBootAt: at, TemplateOKRef: "r0", TemplateFailedRef: "r2", TemplateStrikes: 1, UpdatedAt: at}
	if err := st.TerminalIdentities().Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := st.TerminalIdentities().Get(ctx, "t")
	if err != nil || out != in {
		t.Fatalf("got %+v, %v", out, err)
	}
	zero := domain.TerminalIdentity{ID: "z", TerminalID: "z", ProfileID: "p", InstanceID: "i", Source: "captured", UpdatedAt: at}
	if err := st.TerminalIdentities().Create(ctx, zero); err != nil {
		t.Fatal(err)
	}
	if out, err = st.TerminalIdentities().Get(ctx, "z"); err != nil || !out.TemplateBootAt.IsZero() || out.TemplateBootRef != "" {
		t.Fatalf("zero record %+v, %v", out, err)
	}
}
