# WinPE 适配环境

换网卡型号的适配（ADR-0007，issues 066～068）要让客户机先进一次 WinPE。WinPE 从哪来，按优先级：

## 1. 镜像自带的恢复环境（默认，不需要任何工具）

Windows 镜像的恢复分区里有 `Recovery\WindowsRE\Winre.wim` 和 `boot.sdi`（没有恢复分区时在 `C:\Windows\System32\Recovery\`）。
它就是微软的 WinPE，自带 DISM（含 `/Add-NetAdapter`）、`reg`、`drvload`、`iscsicli`，而且**版本和镜像完全一致**。
服务端在首次适配时把这两个文件从镜像里拷到 `<pool>/nd/files/winpe/<镜像>/`，以后复用。

产品的 PE 脚本和自动匹配的网卡驱动由 wimboot 在启动时注入到 WinPE 的内存盘里，`Winre.wim` 本身不修改，
也不会被分发到镜像以外的地方。

启动配置（BCD）也从镜像里拿：`Windows\Boot\DVD\EFI\BCD`（UEFI）和 `Windows\Boot\DVD\PCAT\BCD`（BIOS）是 Windows
做恢复介质用的通用启动配置，和安装介质里的那份完全相同，提取时一并带出，按客户机固件下发。回报用的 `curl.exe`
同样取自系统分区。**正常情况下什么都不用上传。**

镜像里没有这两份 BCD 时（极少见），用 Windows 自带的 `bcdedit` 生成一份上传作兜底：

```powershell
# 任意一台 Windows，管理员 PowerShell
.\New-NdPeBCD.ps1 -OutDir C:\ndwinpe
```

把 `C:\ndwinpe\BCD` 在镜像详情里上传（只有镜像缺这份文件时才会出现上传入口）。BIOS 与 UEFI 共用这一份：wimboot 启动时按固件改写
`winload.exe/winload.efi`，`bootmgr`/`bootmgfw.efi` 也由 wimboot 从 wim 里自动取出。

## 2. 用 Windows ADK 自建（镜像没有 WinRE 时）

有些镜像制作时删掉了恢复环境。这时在装有 **Windows ADK + Windows PE 附加组件** 的机器上生成：

```powershell
# 管理员 PowerShell；ADK 版本不低于要适配的镜像（C-16）
.\Build-NdWinPE.ps1 -WorkDir C:\ndwinpe-build -OutDir C:\ndwinpe
# WinPE 不认识目标网卡时，把该网卡的 INF 目录做进去
.\Build-NdWinPE.ps1 -WorkDir C:\ndwinpe-build -OutDir C:\ndwinpe -DriverDir C:\drivers\nic
```

产出 `ndwinpe-<内部版本>-<日期>.zip`，含 `boot.wim`、`boot.sdi`、`BCD`、`manifest.json`，同样在镜像详情里上传。
服务端按 manifest 校验哈希，并拒绝给比它新的 Windows 镜像使用。

## 版本规则（C-16）

WinPE 的 DISM 只能处理不比自己新的 Windows。方式 1 天然满足；方式 2 由上传校验与任务前置检查把关。

## 已验证

- 2026-10-06，VMware 三节点、Win11 22621 镜像、UEFI：三件套（`Winre.wim` + `boot.sdi` + 安装介质 BCD）经 wimboot 引导成功，
  并完成了换网卡适配。该镜像 `Windows\Boot\DVD\EFI\BCD` 的哈希与所用安装介质 BCD 完全一致。`New-NdPeBCD.ps1` 生成的 BCD 尚待真机跑一次。
- BIOS 固件路径未验证。

## 不做的事

- 不把微软的 `Winre.wim`/`boot.wim` 打进产品包或发布到仓库。
- 不修改 wim：脚本、驱动、参数全部在启动时注入。
