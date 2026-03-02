@echo off
setlocal enabledelayedexpansion

set ROOT_DIR=%~dp0..
set APP_DIR=%ROOT_DIR%\apps\super-app-flutter
set DIST_DIR=%ROOT_DIR%\dist\windows
if "%NEXORA_API_BASE_URL%"=="" set NEXORA_API_BASE_URL=http://localhost

where flutter >nul 2>nul
if errorlevel 1 (
  echo [build_windows] ERRO: flutter nao encontrado no PATH.
  exit /b 1
)

if not exist "%APP_DIR%\pubspec.yaml" (
  echo [build_windows] ERRO: pubspec.yaml nao encontrado em %APP_DIR%.
  exit /b 1
)

if not exist "%APP_DIR%\windows" (
  call flutter create --platforms=windows "%APP_DIR%"
  if errorlevel 1 exit /b 1
)

call flutter config --enable-windows-desktop
if errorlevel 1 exit /b 1

pushd "%APP_DIR%"
call flutter pub get
if errorlevel 1 (
  popd
  exit /b 1
)

call flutter build windows --release --dart-define=NEXORA_API_BASE_URL=%NEXORA_API_BASE_URL%
if errorlevel 1 (
  popd
  exit /b 1
)
popd

if not exist "%DIST_DIR%" mkdir "%DIST_DIR%"
xcopy /E /I /Y "%APP_DIR%\build\windows\x64\runner\Release\*" "%DIST_DIR%\" >nul

echo [build_windows] Build Windows gerado em %DIST_DIR%
exit /b 0
