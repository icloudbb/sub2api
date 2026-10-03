@echo off
rem Shim for the sub2api task runner; ./make is the same shim for bash.
rem Every task lives in tools/mk (Go). Run `make.bat help` for the command list.
cd /d "%~dp0"

where go >nul 2>nul
if not errorlevel 1 goto run
for /f "tokens=2" %%v in ('findstr /b /c:"go " tools\mk\go.mod') do set "GOWANT=%%v"
echo sub2api needs Go %GOWANT%, and 'go' is not on your PATH. 1>&2
echo Install it from https://go.dev/dl/, or run: winget install GoLang.Go 1>&2
exit /b 1

:run
go run -C tools\mk . %*
exit /b %errorlevel%
