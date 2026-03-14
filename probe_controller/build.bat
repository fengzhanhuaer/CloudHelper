@echo off
echo Building CloudHelper Probe Controller...

if not exist "..\release" mkdir "..\release"

go build -o "..\release\probe_controller.exe"

if %ERRORLEVEL% equ 0 (
    echo [Success] Build complete! Output: ..\release\probe_controller.exe
) else (
    echo [Error] Build failed!
    exit /b 1
)
