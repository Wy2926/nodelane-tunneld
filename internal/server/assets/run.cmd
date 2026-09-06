@echo off
setlocal EnableExtensions DisableDelayedExpansion
rem This endpoint is the parameterless pipe bootstrap. Parameterized commands invoke install.cmd directly.
set "NT_BOOTSTRAP_DIR=%TEMP%\nodelane-tunnel-%RANDOM%-%RANDOM%-%RANDOM%-%RANDOM%"
mkdir "%NT_BOOTSTRAP_DIR%" >nul 2>&1
if errorlevel 1 exit /b %errorlevel%
set "NT_BOOTSTRAP=%NT_BOOTSTRAP_DIR%\install.cmd"
if not defined NT_INSTALL_URL set "NT_INSTALL_URL=https://tunnel.nodelane.net/install.cmd"
curl.exe -fsSL "%NT_INSTALL_URL%" -o "%NT_BOOTSTRAP%"
if errorlevel 1 (
  del /q "%NT_BOOTSTRAP%" >nul 2>&1
  rmdir "%NT_BOOTSTRAP_DIR%" >nul 2>&1
  exit /b 1
)
call "%NT_BOOTSTRAP%"
set "NT_EXIT=%errorlevel%"
del /q "%NT_BOOTSTRAP%" >nul 2>&1
rmdir "%NT_BOOTSTRAP_DIR%" >nul 2>&1
exit /b %NT_EXIT%
