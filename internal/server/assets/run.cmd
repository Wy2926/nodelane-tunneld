@echo off
setlocal
set "NT_BOOTSTRAP=%TEMP%\nodelane-tunnel-%RANDOM%.cmd"
if not defined NT_INSTALL_URL set "NT_INSTALL_URL=https://tunnel.nodelane.net/install.cmd"
curl.exe -fsSL "%NT_INSTALL_URL%" -o "%NT_BOOTSTRAP%"
if errorlevel 1 exit /b %errorlevel%
call "%NT_BOOTSTRAP%"
set "NT_EXIT=%errorlevel%"
del /q "%NT_BOOTSTRAP%" >nul 2>&1
exit /b %NT_EXIT%
