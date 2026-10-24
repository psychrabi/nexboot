@echo off
rem WinPE owns template generation. All registry copying stays on Windows.
set "ND_STAGE=disk"
call "%ND%\ndpe-common.cmd" show disk
> "%ND%\nddisk.txt" echo san policy=OnlineAll
>> "%ND%\nddisk.txt" echo automount enable
diskpart /s "%ND%\nddisk.txt"
call :findos
if not defined OS (
  ping -n 11 127.0.0.1 >nul
  call :findos
)
if not defined OS (
  rem iBFT may already have connected the target without the user-mode service.
  net start msiscsi
  echo msiscsi start exit=!errorlevel!
  iscsicli QAddTargetPortal %ND_PORTAL%
  if defined ND_USERNAME (
    iscsicli QLoginTarget %ND_TARGET% %ND_USERNAME% %ND_PASSWORD%
  ) else (
    iscsicli QLoginTarget %ND_TARGET%
  )
  > "%ND%\nddisk.txt" echo rescan
  diskpart /s "%ND%\nddisk.txt"
  call :findos
)
if not defined OS exit /b 11
echo Adaptation system disk found: !OS!
set "ND_STAGE=environment"
call "%ND%\ndpe-common.cmd" show environment
reg load HKLM\NDSOFTWARE "!OS!\Windows\System32\config\SOFTWARE"
if errorlevel 1 exit /b 17
for /f "tokens=3" %%B in ('reg query "HKLM\NDSOFTWARE\Microsoft\Windows NT\CurrentVersion" /v CurrentBuildNumber 2^>nul') do set "OSBUILD=%%B"
reg unload HKLM\NDSOFTWARE
if errorlevel 1 exit /b 18
if not defined OSBUILD exit /b 19
echo Windows build=!OSBUILD! WinPE build=%ND_PE_BUILD% compatible max=%ND_PE_MAX_BUILD%
if defined ND_PE_MAX_BUILD if !OSBUILD! GTR !ND_PE_MAX_BUILD! exit /b 20
set "ND_STAGE=driver"
call "%ND%\ndpe-common.cmd" show driver
if exist X:\nddrivers (
  dism /Image:!OS!\ /Add-Driver /Driver:X:\nddrivers /Recurse
  if errorlevel 1 (
    echo DISM /Add-Driver exit=!errorlevel!
    exit /b 12
  )
)
set "ND_STAGE=adapter"
call "%ND%\ndpe-common.cmd" show adapter
cscript //nologo "%ND%\ndnic.js" host "%ND_HWID%" "%ND_BUSLOC%" "%ND%\ndhost.ini"
if errorlevel 1 exit /b 13
for /f "usebackq tokens=1,* delims==" %%A in ("%ND%\ndhost.ini") do set "NIC_%%A=%%B"

dism /Image:!OS!\ /Add-NetAdapter /HostAdapter:!NIC_GUID! /BootDriver:ms_tcpip /BootDriver:ms_tcpip6
if errorlevel 1 (
  echo DISM /Add-NetAdapter exit=!errorlevel!
  exit /b 14
)
set "ND_STAGE=registry"
call "%ND%\ndpe-common.cmd" show registry
reg load HKLM\NDOFF "!OS!\Windows\System32\config\SYSTEM"
if errorlevel 1 exit /b 15
cscript //nologo "%ND%\ndnic.js" adapt "%ND_HWID%" "%ND_BUSLOC%" "%ND%\ndpe-result.json" "!OS!"
set "ADAPT_RC=!errorlevel!"
reg unload HKLM\NDOFF
if errorlevel 1 exit /b 16
exit /b !ADAPT_RC!
:findos
rem The server marks this disposable clone before exporting it; never select an unrelated local Windows disk.
set "OS="
for %%D in (C D E F G H I J K L M N O P Q R S T U V W Y Z) do if exist %%D:\ndadapt-target.txt if exist %%D:\Windows\System32\config\SYSTEM set "OS=%%D:"
exit /b 0
