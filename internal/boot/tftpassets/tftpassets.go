// Package tftpassets embeds iPXE/TFTP boot files served to PXE clients, producing a self-contained binary.
//
// Do not use distribution ipxe assets: their snponly.efi contains an embedded chain script
// that ignores the boot filename from DHCP option 67. These assets have no embedded scripts.
// undionly.kpxe is compressed; grep is not a reliable check. Rebuild with scripts/build-undionly.sh.
package tftpassets

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed files/snponly.efi files/undionly.kpxe files/wimboot
var files embed.FS

// Names lists embedded files as expected by the dnsmasq Renderer under TFTPRoot
// (BIOS uses undionly.kpxe, UEFI snponly.efi, and wimboot supports offline WinPE adaptation).
var Names = []string{"snponly.efi", "undionly.kpxe", "wimboot"}

// WriteTo writes embedded boot assets to dir and overwrites existing files at startup to prevent mismatched distribution assets.
func WriteTo(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tftpassets: create %s: %w", dir, err)
	}
	for _, name := range Names {
		data, err := fs.ReadFile(files, "files/"+name)
		if err != nil {
			return fmt.Errorf("tftpassets: read embedded %s: %w", name, err)
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			return fmt.Errorf("tftpassets: write %s: %w", dst, err)
		}
	}
	return nil
}
