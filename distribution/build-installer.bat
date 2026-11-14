@echo off
REM 本地构建 MSI（与 CI 等价；需先: dotnet tool install --global wix）
REM 用法: distribution\build-installer.bat [版本号，默认 0.0.1]，可从任意目录调用
cd /d %~dp0\.. || exit /b 1

set VERSION=%~1
if "%VERSION%"=="" set VERSION=0.0.1

if not exist build mkdir build
go build -ldflags "-s -w -X main.version=%VERSION%" -o ./build/ ./balance_connector

if not exist build\nssm.exe (
    echo downloading nssm...
    curl -fL -o nssm.zip https://nssm.cc/release/nssm-2.24.zip
    tar -xf nssm.zip
    copy /y nssm-2.24\win64\nssm.exe build\nssm.exe
)

wix build installer.wxs -d Version=%VERSION% -out "build\balance_connector-%VERSION%.msi" || exit /b 1
echo MSI: build\balance_connector-%VERSION%.msi
