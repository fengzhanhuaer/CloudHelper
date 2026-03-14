@echo off
echo Building CloudHelper Probe Panel...

if not exist "..\release" mkdir "..\release"

go build -ldflags="-s -w" -o "..\release\probe_panel.exe"

if %ERRORLEVEL% equ 0 (
    echo [Success] Build complete! Output saved to ..\release\probe_panel.exe
) else (
    echo [Error] Build failed!
)
