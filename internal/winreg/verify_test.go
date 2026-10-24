package winreg

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual merge/readback boundary without requiring a Windows hive or hivex installation.
func TestCommitMergedReadsOneSubtreeAndChecksAllValues(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			dir := t.TempDir()
			hive := filepath.Join(dir, "SYSTEM")
			b := make([]byte, 512)
			copy(b, "regf")
			var sum uint32
			for i := 0; i < 508; i += 4 {
				sum ^= binary.LittleEndian.Uint32(b[i:])
			}
			binary.LittleEndian.PutUint32(b[508:], sum)
			if err := os.WriteFile(hive, b, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("REG_READBACK", filepath.Join(dir, "readback.reg"))
			t.Setenv("REG_CALLS", filepath.Join(dir, "calls"))
			t.Setenv("REG_CORRUPT", fmt.Sprint(corrupt))
			script := `#!/bin/sh
printf '%s\n' "$1" >> "$REG_CALLS"
if [ "$1" = --merge ]; then
 cp "$5" "$REG_READBACK"
else
 if [ "$REG_CORRUPT" = true ]; then sed 's/dword:00000001/dword:00000002/g' "$REG_READBACK"; else cat "$REG_READBACK"; fi
fi
`
			if err := os.WriteFile(filepath.Join(dir, "hivexregedit"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var patch strings.Builder
			for i := 0; i < 80; i++ {
				fmt.Fprintf(&patch, "[%s\\ControlSet001\\Services\\Test\\N%d]\n\"Value\"=dword:00000001\n", prefix, i)
			}
			err := commitMerged(context.Background(), hive, patch.String(), `PCI\VEN_8086&DEV_100F\4&abc&0&00E0`, "ControlSet001", "0002")
			if corrupt {
				if err == nil {
					t.Fatal("changed value bytes were accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(os.Getenv("REG_CALLS"))
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(calls), "--export"); n != 2 {
				t.Fatalf("readback launched %d hive exports, want 2", n)
			}
		})
	}
}

func TestGenerationFailureDistinguishesInputErrorsFromTransientIO(t *testing.T) {
	if err := ApplyGenerated(context.Background(), t.TempDir(), "missing", GenerateSpec{}); !IsGenerationFailure(err) {
		t.Fatalf("invalid input was not classified: %v", err)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, os.ErrPermission, skipTemplate("missing driver"), fmt.Errorf("remote: %s", InstancePresentPrefix)} {
		if IsGenerationFailure(err) {
			t.Fatalf("non-generation failure classified: %v", err)
		}
	}
	if !IsGenerationFailure(fmt.Errorf("remote storage: %sbroken template", generationFailurePrefix)) {
		t.Fatal("remote classification lost")
	}
}

func TestRegistryReadbackNormalizesEncodingButPreservesTypeAndBytes(t *testing.T) {
	for _, tt := range []struct {
		a, b  string
		equal bool
	}{
		{`"网卡"`, "hex(1):" + utf16Hex("网卡"), true},
		{"dword:00000001", "hex(4):01,00,00,00", true},
		{"hex(3):01,00,00,00", "dword:00000001", false},
		{"hex(7):61,00,00,00,00,00", "hex(7):62,00,00,00,00,00", false},
	} {
		a, ab, ae := registryValue(tt.a)
		b, bb, be := registryValue(tt.b)
		if got := ae == nil && be == nil && a == b && bytes.Equal(ab, bb); got != tt.equal {
			t.Fatalf("%s vs %s equal=%v", tt.a, tt.b, got)
		}
	}
}

// hivexregedit interprets quoted UTF-8 bytes as individual characters; hex(1) states exact UTF-16LE REG_SZ bytes.
func TestGeneratedUnicodeStringsCarryExplicitUTF16LEBytes(t *testing.T) {
	if got, want := quoteReg("网卡"), "hex(1):51,7f,61,53,00,00"; got != want {
		t.Fatalf("Unicode REG_SZ serialization = %s, want %s", got, want)
	}
	if got := quoteReg("Ethernet"); got != `"Ethernet"` {
		t.Fatalf("ASCII serialization changed: %s", got)
	}
}

func TestTemplateQuotedTextPreservesUnicodeEscapesAndTypedBytes(t *testing.T) {
	const key = prefix + `\ControlSet001\Services\Sample`
	patch := "[" + key + "]\n" + `"Label"="网卡 \\path \"label\""` + "\n" + `"Expand"=hex(2):25,00,58,00,25,00,00,00` + "\n"
	out, err := encodeTemplateStrings(patch)
	if err != nil {
		t.Fatal(err)
	}
	k := findKey(parseReg(out), key)
	if got := decodeRegString(k.get("Label")); got != "网卡 \\path \"label\"" {
		t.Fatalf("escaped Unicode changed: %q", got)
	}
	if k.get("Expand") != "hex(2):25,00,58,00,25,00,00,00" {
		t.Fatal("REG_EXPAND_SZ bytes or type changed")
	}
	if !strings.HasPrefix(k.get("Label"), "hex(1):") {
		t.Fatal("UTF-8 quoted text passed to hivex")
	}
	for _, p := range []string{"[" + key + `\网卡]` + "\n", "[" + key + "]\n" + `"网卡"="value"` + "\n"} {
		if _, err := encodeTemplateStrings(p); err == nil {
			t.Fatal("unsupported non-ASCII key/value name accepted")
		}
	}
}
