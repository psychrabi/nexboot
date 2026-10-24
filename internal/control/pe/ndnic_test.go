package pe

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
)

// Execute the served JScript entry point with Windows platform adapters. Native
// reg.exe/DISM behavior is checked separately by running the payload in WinPE.
func runNICPayload(t *testing.T, scenario string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("JScript logic check requires Node (also required by the web build)")
	}
	svc, st, _ := newSessionFixture(t)
	ctx := context.Background()
	res, err := svc.StartProbe(ctx, "t1", ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.PESessions().Get(ctx, res.SessionID)
	payload, err := svc.File(ctx, sess.Token, "ndnic.js", "efi")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/ndnic.cjs", scenario)
	cmd.Stdin = bytes.NewReader(payload.Content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("JScript %s: %v\n%s", scenario, err, out)
	}
}

func TestNICPayloadCompletesNewDriverServiceAndPreservesExistingService(t *testing.T) {
	for _, scenario := range []string{"missing-service", "missing-binary", "existing-service"} {
		t.Run(scenario, func(t *testing.T) { runNICPayload(t, scenario) })
	}
}

func TestNICPayloadPreservesDetailedSelfCheckFailure(t *testing.T) {
	runNICPayload(t, "preserve-failure")
}

func TestNICPayloadMatchesCurrentLocationWhenHardwareIDHasOtherSlots(t *testing.T) {
	for _, scenario := range []string{"multiple-slots", "ambiguous-location"} {
		t.Run(scenario, func(t *testing.T) { runNICPayload(t, scenario) })
	}
}

func TestNICPayloadReadsNativeLocationProperty(t *testing.T) {
	for _, scenario := range []string{"native-location-property", "native-location-property-other-slot", "native-location-property-alias", "native-location-property-ambiguous", "native-location-property-unmatched", "native-location-property-wrong-type", "native-location-property-missing"} {
		t.Run(scenario, func(t *testing.T) { runNICPayload(t, scenario) })
	}
}

// iPXE 的 ${net0/busloc} 是十六进制（0x300）；按十进制解析会读成 0，启动网卡永远匹配不上。
func TestNICPayloadMatchesBootNICByPCILocation(t *testing.T) {
	// 只有一块同型号网卡时位置对不上也照用；两块时按位置区分，都对不上则列出各自位置。
	for _, scenario := range []string{"hex-busloc", "decimal-busloc", "busloc-mismatch", "plain-location", "two-nics-hex-busloc", "two-nics-no-match"} {
		t.Run(scenario, func(t *testing.T) { runNICPayload(t, scenario) })
	}
}

func TestNICPayloadDistinguishesEnvironmentFailures(t *testing.T) {
	for _, scenario := range []string{"environment-load", "environment-unload", "environment-missing", "environment-version", "adapter-dism"} {
		t.Run(scenario, func(t *testing.T) { runNICPayload(t, scenario) })
	}
}
