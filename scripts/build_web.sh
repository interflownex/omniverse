#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${ROOT_DIR}/apps/super-app-flutter"
DIST_DIR="${ROOT_DIR}/dist/pwa"
API_BASE="${NEXORA_API_BASE_URL:-http://localhost}"

if ! command -v flutter >/dev/null 2>&1; then
  echo "[build_web] ERRO: flutter não encontrado no PATH."
  exit 1
fi

if [[ ! -f "${APP_DIR}/pubspec.yaml" ]]; then
  echo "[build_web] ERRO: pubspec.yaml não encontrado em ${APP_DIR}."
  exit 1
fi

mkdir -p "${DIST_DIR}"

# Garante scaffold Web/PWA caso esteja ausente.
if [[ ! -d "${APP_DIR}/web" ]]; then
  flutter create --platforms=web "${APP_DIR}"
fi

pushd "${APP_DIR}" >/dev/null
flutter pub get
flutter build web \
  --release \
  --pwa-strategy=offline-first \
  --dart-define=NEXORA_API_BASE_URL="${API_BASE}"
popd >/dev/null

rm -rf "${DIST_DIR}"/*
cp -r "${APP_DIR}/build/web/." "${DIST_DIR}/"

echo "[build_web] PWA gerada em ${DIST_DIR}"
