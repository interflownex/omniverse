#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${ROOT_DIR}/apps/super-app-flutter"
DIST_DIR="${ROOT_DIR}/dist/android"
API_BASE="${NEXORA_API_BASE_URL:-http://localhost}"

if ! command -v flutter >/dev/null 2>&1; then
  echo "[build_android] ERRO: flutter não encontrado no PATH."
  exit 1
fi

if [[ ! -f "${APP_DIR}/pubspec.yaml" ]]; then
  echo "[build_android] ERRO: pubspec.yaml não encontrado em ${APP_DIR}."
  exit 1
fi

mkdir -p "${DIST_DIR}"

# Garante scaffold Android caso o projeto tenha sido criado apenas com código-fonte.
if [[ ! -d "${APP_DIR}/android" ]]; then
  flutter create --platforms=android "${APP_DIR}"
fi

pushd "${APP_DIR}" >/dev/null
flutter pub get
flutter build apk --release --dart-define=NEXORA_API_BASE_URL="${API_BASE}"
popd >/dev/null

APK_PATH="${APP_DIR}/build/app/outputs/flutter-apk/app-release.apk"
if [[ ! -f "${APK_PATH}" ]]; then
  echo "[build_android] ERRO: APK não encontrado em ${APK_PATH}."
  exit 1
fi

cp -f "${APK_PATH}" "${DIST_DIR}/nexora-super-app-release.apk"

echo "[build_android] APK gerado em ${DIST_DIR}/nexora-super-app-release.apk"
