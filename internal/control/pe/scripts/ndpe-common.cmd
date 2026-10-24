@echo off
rem Shared network, injected driver loading and reporting commands.
rem Prefer curl.exe from the environment; otherwise use ndpe-report.js.
setlocal EnableExtensions EnableDelayedExpansion
set "ND=%SYSTEMROOT%\System32"
if /i "%~1"=="init" goto init
if /i "%~1"=="post" goto post
if /i "%~1"=="upload" goto upload
if /i "%~1"=="result" goto result
if /i "%~1"=="show" goto show
echo unknown subcommand %~1
exit /b 2

:show
rem CON keeps operator messages visible even when the caller captures tool output.
cscript //nologo "%ND%\ndpe-report.js" show "%~2" "%ND_MODE%" "%~3" "%~4" "%ND%\ndpe-result.json" >CON 2>&1
exit /b %errorlevel%

:init
if exist "%ND%\nd-driver-files.txt" for /f "usebackq tokens=1,* delims=|" %%A in ("%ND%\nd-driver-files.txt") do (
 for %%P in ("X:\nddrivers\%%B") do if not exist "%%~dpP" mkdir "%%~dpP"
 copy /y "%ND%\%%A" "X:\nddrivers\%%B"
)
wpeinit
wpeutil InitializeNetwork
rem The environment may already contain the NIC driver. Verify actual reporting
rem before attempting injected drivers; drvload errors alone do not prove readiness.
call :post x "%ND_REPORT%/booted" 1
if not errorlevel 1 (
 echo Network ready; injected driver loading is not needed.
 exit /b 0
)
for /r X:\nddrivers %%F in (*.inf) do (
 drvload "%%F"
 echo drvload exit=!errorlevel! file=%%F
)
wpeutil InitializeNetwork
call :post x "%ND_REPORT%/booted"
exit /b !errorlevel!

:post
set /a ND_TRY=0
set "ND_MAX_TRIES=10"
if "%~3"=="1" set "ND_MAX_TRIES=1"
:post_retry
if exist "%ND%\curl.exe" (
  "%ND%\curl.exe" --silent --show-error --max-time 30 --fail -X POST -d "" "%~2" && exit /b 0
) else (
  cscript //nologo "%ND%\ndpe-report.js" "%~2" && exit /b 0
)
set /a ND_TRY+=1
if %ND_TRY% GEQ %ND_MAX_TRIES% exit /b 1
ping -n 4 127.0.0.1 >nul 2>&1
goto post_retry

:upload
set /a ND_TRY=0
:upload_retry
if exist "%ND%\curl.exe" (
  "%ND%\curl.exe" --silent --show-error --max-time 120 --fail --data-binary "@%~3" "%~2" && exit /b 0
) else (
  cscript //nologo "%ND%\ndpe-report.js" "%~2" "%~3" && exit /b 0
)
set /a ND_TRY+=1
if %ND_TRY% GEQ 5 exit /b 1
ping -n 4 127.0.0.1 >nul 2>&1
goto upload_retry

:result
rem %2=url %3=true|false %4=stage %5=message
> "%ND%\ndpe-result.json" echo {"ok":%~3,"stage":"%~4","message":"%~5"}
call :upload x "%~2" "%ND%\ndpe-result.json"
exit /b %errorlevel%
