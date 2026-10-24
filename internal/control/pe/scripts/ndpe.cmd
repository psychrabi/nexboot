@echo off
rem Keep the environment's native code page for WSH console output.
rem Entry point injected by wimboot into System32 and launched by winpeshl.ini.
rem Read parameters, initialize networking, run the mode, report and shut down.
setlocal EnableExtensions EnableDelayedExpansion
set "ND=%SYSTEMROOT%\System32"
set "ND_LOG=%ND%\ndpe-console.log"
for /f "usebackq tokens=1,* delims==" %%A in ("%ND%\ndpe.ini") do set "ND_%%A=%%B"
echo ND BEGIN %DATE% %TIME% mode=%ND_MODE% > "%ND_LOG%"
chcp >> "%ND_LOG%"
title NexBoot WinPE
call "%ND%\ndpe-common.cmd" show start
call "%ND%\ndpe-common.cmd" show network
set "ND_STAGE=network"
call "%ND%\ndpe-common.cmd" init >> "%ND_LOG%" 2>&1
set "ND_EXIT=!errorlevel!"
if not "!ND_EXIT!"=="0" (
  set "ND_EXIT=10"
  goto report
)

set "ND_EXIT=0"
if /i "%ND_MODE%"=="probe" (
  call :probe >> "%ND_LOG%" 2>&1
  set "ND_EXIT=!errorlevel!"
) else if /i "%ND_MODE%"=="adapt" (
 call "%ND%\ndadapt-pe.cmd" >> "%ND_LOG%" 2>&1
 set "ND_EXIT=!errorlevel!"
) else if /i "%ND_MODE%"=="identity" (
 call "%ND%\ndidentity.cmd" >> "%ND_LOG%" 2>&1
 set "ND_EXIT=!errorlevel!"
) else (
  echo unknown mode %ND_MODE% >> "%ND_LOG%"
  set "ND_EXIT=9"
)
:report
echo ND END %DATE% %TIME% exit=!ND_EXIT! >> "%ND_LOG%"

call "%ND%\ndpe-common.cmd" show report
call "%ND%\ndpe-common.cmd" upload "%ND_REPORT%/log/console.log" "%ND_LOG%"
set "ND_CONSOLE_RC=!errorlevel!"
set "ND_DISM_RC=0"
if exist "%SYSTEMROOT%\Logs\DISM\dism.log" (
 call "%ND%\ndpe-common.cmd" upload "%ND_REPORT%/log/dism.log" "%SYSTEMROOT%\Logs\DISM\dism.log"
 set "ND_DISM_RC=!errorlevel!"
)
if not "!ND_EXIT!"=="0" (
 if not defined ND_STAGE set "ND_STAGE=%ND_MODE%"
 cscript //nologo "%ND%\ndnic.js" failure "!ND_STAGE!" "!ND_EXIT!" "%ND%\ndpe-result.json"
)
if exist "%ND%\ndpe-result.json" (
 call "%ND%\ndpe-common.cmd" upload "%ND_REPORT%/result" "%ND%\ndpe-result.json"
 set "ND_RESULT_RC=!errorlevel!"
) else if "!ND_EXIT!"=="0" (
  call "%ND%\ndpe-common.cmd" result "%ND_REPORT%/result" true %ND_MODE% "WinPE boot, network and reporting succeeded"
  set "ND_RESULT_RC=!errorlevel!"
) else (
  call "%ND%\ndpe-common.cmd" result "%ND_REPORT%/result" false %ND_MODE% "Step returned !ND_EXIT!; see logs"
  set "ND_RESULT_RC=!errorlevel!"
)
set "ND_REPORTED=!ND_RESULT_RC!"
if "!ND_RESULT_RC!"=="0" if not "!ND_CONSOLE_RC!!ND_DISM_RC!"=="00" set "ND_REPORTED=logs"
call "%ND%\ndpe-common.cmd" show finish "!ND_EXIT!" "!ND_REPORTED!"
cscript //nologo "%ND%\ndpe-report.js" restart "%ND_MODE%" "!ND_EXIT!" "!ND_REPORTED!"
if not errorlevel 1 (
  wpeutil reboot
  exit /b
)
wpeutil shutdown
exit /b

:probe
rem Probe the version, network and local NIC class instances.
call "%ND%\ndpe-common.cmd" show probe
ver
ipconfig /all
reg query "HKLM\SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}" /s /v MatchingDeviceId
exit /b 0
