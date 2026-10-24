package pe

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
)

func runDisplayPayload(t *testing.T, scenario string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("WinPE display check requires Node")
	}
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	payload, err := svc.File(ctx, sess.Token, "ndpe-report.js", "efi")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/ndpe-display.cjs", scenario)
	cmd.Stdin = bytes.NewReader(payload.Content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("WinPE display %s: %v\n%s", scenario, err, out)
	}
}

func TestPEPayloadExplainsPurposeOnClient(t *testing.T) {
	runDisplayPayload(t, "start")
}

func TestPEPayloadShowsCurrentStageAndDistinguishesFinalOutcome(t *testing.T) {
	for _, scenario := range []string{"stage", "submitted", "failed", "unconfirmed", "missing-log", "identity", "probe", "restart-identity-success", "restart-identity-server-failed", "restart-identity-local-failed", "restart-adapt", "restart-probe"} {
		t.Run(scenario, func(t *testing.T) { runDisplayPayload(t, scenario) })
	}
}
