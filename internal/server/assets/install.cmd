@echo off
setlocal EnableExtensions DisableDelayedExpansion

where curl.exe >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Required command not found: curl.exe" & goto fail
where tar.exe >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Required command not found: tar.exe" & goto fail
where certutil.exe >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Required command not found: certutil.exe" & goto fail
where reg.exe >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Required command not found: reg.exe" & goto fail

set "NT_ARCH_NAME=%PROCESSOR_ARCHITEW6432%"
if not defined NT_ARCH_NAME set "NT_ARCH_NAME=%PROCESSOR_ARCHITECTURE%"
if /I "%NT_ARCH_NAME%"=="AMD64" set "NT_ARCH=amd64"
if /I "%NT_ARCH_NAME%"=="X86_64" set "NT_ARCH=amd64"
if /I "%NT_ARCH_NAME%"=="ARM64" set "NT_ARCH=arm64"
if not defined NT_ARCH set "NT_MESSAGE=Unsupported CPU architecture: %NT_ARCH_NAME%" & goto fail

if not defined LOCALAPPDATA set "LOCALAPPDATA=%USERPROFILE%\AppData\Local"
if not defined LOCALAPPDATA set "NT_MESSAGE=Unable to locate the Windows local application data directory." & goto fail
if not defined NT_RELEASE_BASE set "NT_RELEASE_BASE=https://tunnel.nodelane.net/releases"
if "%NT_RELEASE_BASE:~-1%"=="/" set "NT_RELEASE_BASE=%NT_RELEASE_BASE:~0,-1%"
set "NT_DATA=%LOCALAPPDATA%\nodelane\tunnel"
set "NT_VERSIONS=%NT_DATA%\versions"
if defined NT_BIN_DIR set "NT_CUSTOM_BIN=1"
if not defined NT_BIN_DIR set "NT_BIN_DIR=%LOCALAPPDATA%\nodelane\bin"
set "NT_CURRENT=%NT_DATA%\current"
set "NT_LAUNCHER=%NT_BIN_DIR%\nt.cmd"
set "NT_WORK=%TEMP%\nodelane-tunnel-install-%RANDOM%-%RANDOM%"

mkdir "%NT_WORK%" >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Unable to create temporary directory: %NT_WORK%" & goto fail
mkdir "%NT_VERSIONS%" >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Unable to create installation directory: %NT_VERSIONS%" & goto fail
mkdir "%NT_BIN_DIR%" >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Unable to create command directory: %NT_BIN_DIR%" & goto fail

echo ==^> Checking the latest NodeLane Tunnel client...
curl.exe -fsSL "%NT_RELEASE_BASE%/stable.txt" -o "%NT_WORK%\stable.txt"
if errorlevel 1 set "NT_MESSAGE=Unable to download the latest release version." & goto fail
set "NT_VERSION="
set /p "NT_VERSION="<"%NT_WORK%\stable.txt"
setlocal EnableDelayedExpansion
echo(!NT_VERSION!| findstr.exe /R /X "[0-9A-Za-z._-][0-9A-Za-z._-]*" >nul
if errorlevel 1 endlocal & set "NT_MESSAGE=The server returned an invalid release version." & goto fail
endlocal

set "NT_ASSET=nt_%NT_VERSION%_windows_%NT_ARCH%.zip"
set "NT_INSTALL=%NT_VERSIONS%\%NT_VERSION%\windows-%NT_ARCH%"
set "NT_CLIENT=%NT_INSTALL%\nt.exe"
set "NT_PREVIOUS="
if exist "%NT_CURRENT%" set /p "NT_PREVIOUS="<"%NT_CURRENT%"
set "NT_INSTALLED_VERSION="
if not exist "%NT_CLIENT%" goto download
"%NT_CLIENT%" --version >"%NT_WORK%\installed-version.txt" 2>nul
if errorlevel 1 goto download
set /p "NT_INSTALLED_VERSION="<"%NT_WORK%\installed-version.txt"

if "%NT_INSTALLED_VERSION%"=="%NT_VERSION%" goto installed

:download
set "NT_ARCHIVE=%NT_WORK%\%NT_ASSET%"
set "NT_PACKAGE=%NT_WORK%\package"
mkdir "%NT_PACKAGE%" >nul 2>&1
if errorlevel 1 set "NT_MESSAGE=Unable to create the package staging directory." & goto fail
echo ==^> Downloading NodeLane Tunnel %NT_VERSION% (windows/%NT_ARCH%)...
curl.exe --fail --location --progress-bar "%NT_RELEASE_BASE%/%NT_VERSION%/%NT_ASSET%" -o "%NT_ARCHIVE%"
if errorlevel 1 set "NT_MESSAGE=Unable to download %NT_ASSET%." & goto fail
curl.exe -fsSL "%NT_RELEASE_BASE%/%NT_VERSION%/%NT_ASSET%.sha256" -o "%NT_WORK%\checksum.txt"
if errorlevel 1 set "NT_MESSAGE=Unable to download the package checksum." & goto fail

set "NT_EXPECTED="
for /f "usebackq tokens=1" %%H in ("%NT_WORK%\checksum.txt") do if not defined NT_EXPECTED set "NT_EXPECTED=%%H"
setlocal EnableDelayedExpansion
echo(!NT_EXPECTED!| findstr.exe /R /I /X "[0-9A-F][0-9A-F]*" >nul
if errorlevel 1 endlocal & set "NT_MESSAGE=The server returned an invalid SHA-256 checksum." & goto fail
if "!NT_EXPECTED:~63,1!"=="" endlocal & set "NT_MESSAGE=The server returned an invalid SHA-256 checksum." & goto fail
if not "!NT_EXPECTED:~64,1!"=="" endlocal & set "NT_MESSAGE=The server returned an invalid SHA-256 checksum." & goto fail
endlocal

echo ==^> Verifying package integrity...
set "NT_ACTUAL="
certutil.exe -hashfile "%NT_ARCHIVE%" SHA256 >"%NT_WORK%\actual-checksum.txt"
if errorlevel 1 set "NT_MESSAGE=Unable to calculate the package checksum." & goto fail
for /f "usebackq skip=1 tokens=*" %%H in ("%NT_WORK%\actual-checksum.txt") do if not defined NT_ACTUAL set "NT_ACTUAL=%%H"
set "NT_ACTUAL=%NT_ACTUAL: =%"
if /I not "%NT_EXPECTED%"=="%NT_ACTUAL%" set "NT_MESSAGE=NodeLane Tunnel package checksum verification failed." & goto fail

tar.exe -xf "%NT_ARCHIVE%" -C "%NT_PACKAGE%"
if errorlevel 1 set "NT_MESSAGE=Unable to extract %NT_ASSET%." & goto fail
if not exist "%NT_PACKAGE%\nt.exe" set "NT_MESSAGE=The downloaded package did not contain nt.exe." & goto fail
set "NT_DOWNLOADED_VERSION="
"%NT_PACKAGE%\nt.exe" --version >"%NT_WORK%\downloaded-version.txt" 2>nul
if errorlevel 1 set "NT_MESSAGE=The downloaded client could not start." & goto fail
set /p "NT_DOWNLOADED_VERSION="<"%NT_WORK%\downloaded-version.txt"
if not "%NT_DOWNLOADED_VERSION%"=="%NT_VERSION%" set "NT_MESSAGE=The downloaded client version does not match %NT_VERSION%." & goto fail

if exist "%NT_INSTALL%" rmdir /s /q "%NT_INSTALL%"
mkdir "%NT_VERSIONS%\%NT_VERSION%" >nul 2>&1
move "%NT_PACKAGE%" "%NT_INSTALL%" >nul
if errorlevel 1 set "NT_MESSAGE=Unable to publish the installed client." & goto fail
echo OK NodeLane Tunnel %NT_VERSION% is installed.
goto publish

:installed
echo ==^> Using installed client %NT_VERSION%.

:publish
>"%NT_DATA%\current.tmp" echo %NT_CLIENT%
move /y "%NT_DATA%\current.tmp" "%NT_CURRENT%" >nul
if errorlevel 1 set "NT_MESSAGE=Unable to update the active client." & goto fail

>"%NT_LAUNCHER%.tmp" (
  echo @echo off
  echo setlocal
  echo set /p "NT_CLIENT="^<"%%LOCALAPPDATA%%\nodelane\tunnel\current"
  echo if not exist "%%NT_CLIENT%%" echo NodeLane Tunnel is not installed; run https://tunnel.nodelane.net/run.cmd again. 1^>^&2 ^& exit /b 1
  echo "%%NT_CLIENT%%" %%*
  echo exit /b %%errorlevel%%
)
move /y "%NT_LAUNCHER%.tmp" "%NT_LAUNCHER%" >nul
if errorlevel 1 set "NT_MESSAGE=Unable to install the nt command launcher." & goto fail

if defined NT_CUSTOM_BIN goto path_ready
reg.exe query HKCU\Environment /v Path 2>nul | findstr.exe /I /L /C:"%NT_BIN_DIR%" >nul
if errorlevel 1 goto add_path
goto path_ready

:add_path
set "NT_USER_PATH="
for /f "tokens=2,*" %%A in ('reg.exe query HKCU\Environment /v Path 2^>nul ^| findstr.exe /I /R "^[ ]*Path[ ]"') do set "NT_USER_PATH=%%B"
if defined NT_USER_PATH reg.exe add HKCU\Environment /v Path /t REG_EXPAND_SZ /d "%NT_BIN_DIR%;%NT_USER_PATH%" /f >nul
if not defined NT_USER_PATH reg.exe add HKCU\Environment /v Path /t REG_EXPAND_SZ /d "%NT_BIN_DIR%" /f >nul
if errorlevel 1 set "NT_MESSAGE=Unable to add the nt command to the user PATH." & goto fail
echo ==^> Added %NT_BIN_DIR% to the user PATH.

:path_ready
set "PATH=%NT_BIN_DIR%;%PATH%"
set "NT_PREVIOUS_DIR="
if defined NT_PREVIOUS for %%P in ("%NT_PREVIOUS%") do set "NT_PREVIOUS_DIR=%%~dpP"
for /d %%V in ("%NT_VERSIONS%\*") do for /d %%P in ("%%~fV\*") do if /I not "%%~fP"=="%NT_INSTALL%" if /I not "%%~fP\"=="%NT_PREVIOUS_DIR%" rmdir /s /q "%%~fP" >nul 2>&1

if exist "%NT_WORK%" rmdir /s /q "%NT_WORK%"
"%NT_CLIENT%" %*
exit /b %errorlevel%

:fail
if defined NT_WORK if exist "%NT_WORK%" rmdir /s /q "%NT_WORK%"
if not defined NT_MESSAGE set "NT_MESSAGE=NodeLane Tunnel installation failed."
echo ERROR %NT_MESSAGE% 1>&2
exit /b 1
