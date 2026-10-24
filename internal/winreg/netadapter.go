package winreg

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inspect the binary copyServiceBinary will retain, not merely the repository version.
func systemBootBinary(root, inf, name string) (string, error) {
	repo, err := findNamedFile(filepath.Dir(inf), name, 2)
	if err != nil {
		return "", err
	}
	system, err := foldPath(root, []string{"Windows", "System32"})
	if err != nil {
		return "", err
	}
	dirs, err := os.ReadDir(system)
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		if !dir.IsDir() || !strings.EqualFold(dir.Name(), "drivers") {
			continue
		}
		drivers := filepath.Join(system, dir.Name())
		files, err := os.ReadDir(drivers)
		if err != nil {
			return "", err
		}
		for _, file := range files {
			if !file.IsDir() && strings.EqualFold(file.Name(), name) {
				return filepath.Join(drivers, file.Name()), nil
			}
		}
	}
	return repo, nil
}

// A class binding is stronger evidence than an incidental string or KmdfService.
// WDF_CLASS_BIND_INFO's amd64 prefix is Size, padding, and ClassName (VA).
// https://github.com/microsoft/Windows-Driver-Frameworks/blob/main/src/framework/shared/inc/private/common/fxldr.h
func bindsNetAdapterCx(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 {
		return false, fmt.Errorf("驱动文件不是有效的 amd64 SYS")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("无法读取 SYS 的 PE 结构：%w", err)
	}
	defer f.Close()
	h, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || f.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || len(f.Sections) == 0 {
		return false, fmt.Errorf("驱动文件不是有效的 amd64 SYS")
	}
	for _, s := range f.Sections {
		if uint64(s.Offset)+uint64(s.Size) > uint64(len(data)) {
			return false, fmt.Errorf("SYS 的 PE 节超出文件范围")
		}
	}
	className := []byte("N\x00e\x00t\x00A\x00d\x00a\x00p\x00t\x00e\x00r\x00C\x00x\x00\x00\x00")
	for _, s := range f.Sections {
		section := data[int(s.Offset):int(uint64(s.Offset)+uint64(s.Size))]
		for i := 0; i+16 <= len(section); i += 8 {
			size := binary.LittleEndian.Uint32(section[i:])
			if size < 80 || uint64(size) > uint64(len(section)-i) {
				continue
			}
			ptr := binary.LittleEndian.Uint64(section[i+8:])
			if ptr < h.ImageBase {
				continue
			}
			rva := ptr - h.ImageBase
			for _, target := range f.Sections {
				if rva < uint64(target.VirtualAddress) {
					continue
				}
				off := rva - uint64(target.VirtualAddress)
				if off > uint64(target.Size) || uint64(len(className)) > uint64(target.Size)-off {
					continue
				}
				at := uint64(target.Offset) + off
				if bytes.EqualFold(data[at:at+uint64(len(className))], className) {
					if size != 80 {
						return false, fmt.Errorf("NetAdapterCx 的 WDF 类绑定结构大小 %d 尚不能核实，请使用兼容的传统 NDIS 无盘驱动", size)
					}
					return true, nil
				}
			}
		}
	}
	return false, nil
}
