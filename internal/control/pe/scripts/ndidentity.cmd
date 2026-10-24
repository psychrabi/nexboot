@echo off
set "ND_STAGE=identity"
call "%ND%\ndpe-common.cmd" show identity
cscript //nologo "%ND%\ndnic.js" identity "%ND_HWID%" "%ND_BUSLOC%" "%ND%\ndpe-result.json"
exit /b %errorlevel%
