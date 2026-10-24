package install

import (
	"bytes"
	"fmt"
)

// casperOverlayName 是附加给 Ubuntu 安装内核的 initrd，iPXE 与内核命令行的 initrd= 都用这个名字。
const casperOverlayName = "nd-iscsi.cpio"

// casper 自己不登录 iSCSI，只会一直找光驱；这个脚本在它找安装介质之前按 iPXE 写的 iBFT 登录，
// 同一 target 的只读 LUN 1（ISO）随之出现。
const casperISCSIScript = `#!/bin/sh
case "$1" in prereqs) exit 0;; esac
modprobe iscsi_tcp
modprobe iscsi_ibft
iscsistart -b || echo "NexBoot: iSCSI login from iBFT failed; the installer cannot find its ISO" > /dev/console
udevadm settle --timeout=30 || true
`

// casper-premount 的 ORDER 会被 source；这份替换原 ORDER，其余脚本按编号顺序照跑，不写死某张 ISO 的脚本名。
const casperPremountOrder = `/scripts/casper-premount/05nexboot-iscsi "$@"
if [ -e /conf/param.conf ]; then . /conf/param.conf; fi
for nd_script in /scripts/casper-premount/[0-9]*; do
	if [ "$nd_script" = /scripts/casper-premount/05nexboot-iscsi ] || [ ! -x "$nd_script" ]; then continue; fi
	"$nd_script" "$@"
	if [ -e /conf/param.conf ]; then . /conf/param.conf; fi
done
`

// casperISCSIOverlay 生成附加 initrd（newc cpio）；内核按加载顺序解开，同名文件以它为准。
type cpioEntry struct {
	name string
	mode uint32
	data string
}

func casperISCSIOverlay() []byte {
	entries := []cpioEntry{
		{"scripts", 0o040755, ""},
		{"scripts/casper-premount", 0o040755, ""},
		{"scripts/casper-premount/05nexboot-iscsi", 0o100755, casperISCSIScript},
		{"scripts/casper-premount/ORDER", 0o100644, casperPremountOrder},
		{"TRAILER!!!", 0, ""},
	}
	return newcArchive(entries)
}

func newcArchive(entries []cpioEntry) []byte {
	var b bytes.Buffer
	for i, e := range entries {
		nlink := 1
		if e.mode&0o040000 != 0 {
			nlink = 2
		}
		fmt.Fprintf(&b, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			i+1, e.mode, 0, 0, nlink, 0, len(e.data), 0, 0, 0, 0, len(e.name)+1, 0)
		b.WriteString(e.name)
		b.WriteByte(0)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
		b.WriteString(e.data)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	return b.Bytes()
}
