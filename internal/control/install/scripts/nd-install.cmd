@echo off
setlocal EnableExtensions DisableDelayedExpansion
title NexBoot - Installing Windows
set "ND=%SYSTEMROOT%\System32"
if exist "%ND%\nd-driver-files.txt" for /f "usebackq tokens=1,* delims=|" %%A in ("%ND%\nd-driver-files.txt") do (
 for %%P in ("X:\nddrivers\%%B") do if not exist "%%~dpP" mkdir "%%~dpP"
 copy /y "%ND%\%%A" "X:\nddrivers\%%B" >nul
)
echo NexBoot: preparing network. This computer is creating a system image.
wpeinit
wpeutil InitializeNetwork
for /r X:\nddrivers %%F in (*.inf) do (echo %%F& drvload "%%F") >>"%ND%\nd-drvload.log" 2>&1
wpeutil InitializeNetwork
cscript //nologo "%ND%\nd-install.js"
if errorlevel 1 (
 echo Installation stopped. The reason is shown above.
 echo This computer will shut down in 2 minutes.
 ping -n 121 127.0.0.1 >nul
 wpeutil shutdown
 exit /b 1
)
echo Installation files prepared. Restarting into Windows initialization.
wpeutil reboot
