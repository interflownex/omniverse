#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
ADMIN_DIR="${ROOT_DIR}/apps/admin-god-mode"
LOG_DIR="${ROOT_DIR}/logs"
RUNTIME_DIR="${ROOT_DIR}/runtime"
SEED_FILE="${ROOT_DIR}/db/sql/seed.sql"
SEED_ON_START="${SEED_ON_START:-1}"
REBUILD_ON_START="${REBUILD_ON_START:-0}"
ADMIN_MODE="${ADMIN_MODE:-preview}"
ENABLE_NGINX="${ENABLE_NGINX:-0}"
START_APK_DOWNLOAD_SERVER="${START_APK_DOWNLOAD_SERVER:-1}"
APK_DOWNLOAD_PORT="${APK_DOWNLOAD_PORT:-8100}"
APK_DOWNLOAD_DIR="${ROOT_DIR}/dist/android"
HEALTHCHECK_TIMEOUT_SECONDS="${HEALTHCHECK_TIMEOUT_SECONDS:-180}"
HEALTHCHECK_INTERVAL_SECONDS="${HEALTHCHECK_INTERVAL_SECONDS:-3}"
LOCK_FILE="${RUNTIME_DIR}/start_nexora.lock"
SUMMARY_FILE="${RUNTIME_DIR}/start-summary.txt"

mkdir -p "${LOG_DIR}" "${RUNTIME_DIR}"

log() {
  echo "[start_nexora] $*"
}

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  log "ERRO: compose não encontrado em ${COMPOSE_FILE}."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  log "ERRO: Docker não encontrado."
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  log "ERRO: Docker daemon indisponível."
  exit 1
fi

acquire_lock() {
  if command -v flock >/dev/null 2>&1; then
    exec 9>"${LOCK_FILE}"
    if ! flock -n 9; then
      log "ERRO: já existe outro start_nexora em execução."
      exit 1
    fi
  fi
}

cleanup_pid_file() {
  local pid_file="$1"
  local label="$2"
  if [[ ! -f "${pid_file}" ]]; then
    return
  fi

  local pid
  pid="$(tr -dc '0-9' < "${pid_file}" || true)"
  if [[ -n "${pid}" ]] && kill -0 "${pid}" >/dev/null 2>&1; then
    log "Encerrando processo residual ${label} (pid ${pid})..."
    kill "${pid}" >/dev/null 2>&1 || true
    sleep 1
    if kill -0 "${pid}" >/dev/null 2>&1; then
      kill -9 "${pid}" >/dev/null 2>&1 || true
    fi
  fi

  rm -f "${pid_file}"
}

cleanup_runtime_state() {
  cleanup_pid_file "${RUNTIME_DIR}/admin-god-mode.pid" "admin-god-mode"
  cleanup_pid_file "${RUNTIME_DIR}/apk-download-server.pid" "apk-download-server"
  cleanup_pid_file "${RUNTIME_DIR}/nexora-core.pid" "nexora-core"
  rm -f "${SUMMARY_FILE}" "${LOCK_FILE}"
}

wait_for_postgres_ready() {
  local deadline=$((SECONDS + HEALTHCHECK_TIMEOUT_SECONDS))
  until docker exec nexora-postgres pg_isready -U nexora -d nexora_pay >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      log "ERRO: timeout aguardando postgres ficar pronto."
      return 1
    fi
    sleep "${HEALTHCHECK_INTERVAL_SECONDS}"
  done
}

wait_for_http_ok() {
  local label="$1"
  local url="$2"
  local deadline=$((SECONDS + HEALTHCHECK_TIMEOUT_SECONDS))

  until [[ "$(curl -s -o /dev/null -w '%{http_code}' "${url}" || true)" == "200" ]]; do
    if (( SECONDS >= deadline )); then
      log "ERRO: timeout aguardando ${label} (${url})."
      return 1
    fi
    sleep "${HEALTHCHECK_INTERVAL_SECONDS}"
  done
}

healthcheck_stack() {
  local checks=(
    "nexora-me|http://127.0.0.1:8081/healthz"
    "nexora-pay|http://127.0.0.1:8082/healthz"
    "open-finance|http://127.0.0.1:8083/healthz"
    "nexora-social|http://127.0.0.1:8084/healthz"
    "nexora-media|http://127.0.0.1:8085/healthz"
    "nexora-chat|http://127.0.0.1:8086/healthz"
    "nexora-stock|http://127.0.0.1:8087/healthz"
    "nexora-place|http://127.0.0.1:8088/healthz"
    "nexora-move|http://127.0.0.1:8089/healthz"
    "nexora-food|http://127.0.0.1:8090/healthz"
    "nexora-business|http://127.0.0.1:8091/healthz"
    "nexora-plug|http://127.0.0.1:8092/healthz"
    "nexora-up|http://127.0.0.1:8093/healthz"
    "document-engine|http://127.0.0.1:8094/healthz"
    "nexora-life|http://127.0.0.1:8095/healthz"
    "nexora-trainer|http://127.0.0.1:8096/healthz"
    "nexora-school-job|http://127.0.0.1:8097/healthz"
    "persona-burnout|http://127.0.0.1:8098/healthz"
    "minio|http://127.0.0.1:9000/minio/health/live"
  )

  for check in "${checks[@]}"; do
    local label="${check%%|*}"
    local url="${check#*|}"
    wait_for_http_ok "${label}" "${url}"
  done
}

start_admin_panel() {
  if [[ "${ADMIN_MODE}" == "skip" ]]; then
    log "Admin panel desativado (ADMIN_MODE=skip)."
    return
  fi

  if [[ ! -d "${ADMIN_DIR}" ]]; then
    log "Aviso: ${ADMIN_DIR} não encontrado. Painel admin ignorado."
    return
  fi

  if ! command -v node >/dev/null 2>&1; then
    if [[ -s "${HOME}/.nvm/nvm.sh" ]]; then
      # shellcheck disable=SC1090
      . "${HOME}/.nvm/nvm.sh" >/dev/null 2>&1 || true
      if command -v nvm >/dev/null 2>&1; then
        nvm use default >/dev/null 2>&1 || true
      fi
    fi
  fi

  if ! command -v npm >/dev/null 2>&1; then
    log "Aviso: npm não encontrado. Painel admin não iniciado."
    return
  fi

  if ! command -v node >/dev/null 2>&1; then
    log "Aviso: node não encontrado no ambiente atual. Painel admin não iniciado."
    return
  fi

  if [[ ! -d "${ADMIN_DIR}/node_modules" ]]; then
    log "Instalando dependências do painel admin..."
    if ! (cd "${ADMIN_DIR}" && npm install --no-fund --no-audit); then
      log "Aviso: falha ao instalar dependências do painel admin."
      return
    fi
  fi

  case "${ADMIN_MODE}" in
    preview)
      log "Gerando build do painel admin..."
      if ! (cd "${ADMIN_DIR}" && npm run build >/dev/null); then
        log "Aviso: falha no build do painel admin; mantendo stack sem painel web."
        return
      fi
      log "Subindo painel admin (preview) em http://localhost:5173 ..."
      (
        cd "${ADMIN_DIR}"
        nohup npm run preview -- --host 0.0.0.0 --port 5173 > "${LOG_DIR}/admin-god-mode.log" 2>&1 &
        echo $! > "${RUNTIME_DIR}/admin-god-mode.pid"
      )
      ;;
    dev)
      log "Subindo painel admin (dev) em http://localhost:5173 ..."
      (
        cd "${ADMIN_DIR}"
        nohup npm run dev -- --host 0.0.0.0 --port 5173 > "${LOG_DIR}/admin-god-mode.log" 2>&1 &
        echo $! > "${RUNTIME_DIR}/admin-god-mode.pid"
      )
      ;;
    *)
      log "ERRO: ADMIN_MODE inválido (${ADMIN_MODE}). Use: preview | dev | skip."
      exit 1
      ;;
  esac

  if ! wait_for_http_ok "admin-god-mode" "http://127.0.0.1:5173"; then
    log "Aviso: painel admin não respondeu em http://127.0.0.1:5173; seguindo sem painel."
    cleanup_pid_file "${RUNTIME_DIR}/admin-god-mode.pid" "admin-god-mode"
  fi
}

start_apk_download_server() {
  local apk_path="${APK_DOWNLOAD_DIR}/nexora-super-app-release.apk"
  local apk_url_file="${RUNTIME_DIR}/apk-download-url.txt"

  rm -f "${apk_url_file}"

  if [[ "${START_APK_DOWNLOAD_SERVER}" != "1" ]]; then
    log "Servidor de APK desativado (START_APK_DOWNLOAD_SERVER=${START_APK_DOWNLOAD_SERVER})."
    return
  fi

  if ! command -v python3 >/dev/null 2>&1; then
    log "Aviso: python3 não encontrado. Servidor de download APK não iniciado."
    return
  fi

  if [[ ! -f "${apk_path}" ]]; then
    log "Aviso: APK não encontrado em ${apk_path}. Servidor de download não iniciado."
    return
  fi

  log "Subindo servidor de download APK em http://127.0.0.1:${APK_DOWNLOAD_PORT} ..."
  nohup python3 -m http.server "${APK_DOWNLOAD_PORT}" --bind 127.0.0.1 --directory "${APK_DOWNLOAD_DIR}" > "${LOG_DIR}/apk-download-server.log" 2>&1 &
  echo $! > "${RUNTIME_DIR}/apk-download-server.pid"

  local download_url="http://127.0.0.1:${APK_DOWNLOAD_PORT}/nexora-super-app-release.apk"
  if ! wait_for_http_ok "apk-download-server" "${download_url}"; then
    log "Aviso: servidor de download APK não respondeu; seguindo sem link local de APK."
    cleanup_pid_file "${RUNTIME_DIR}/apk-download-server.pid" "apk-download-server"
    return
  fi

  echo "${download_url}" > "${apk_url_file}"
}

write_summary() {
  local admin_url="disabled"
  local apk_url="disabled"

  if [[ -f "${RUNTIME_DIR}/admin-god-mode.pid" ]]; then
    admin_url="http://localhost:5173"
  fi
  if [[ -f "${RUNTIME_DIR}/apk-download-url.txt" ]]; then
    apk_url="$(cat "${RUNTIME_DIR}/apk-download-url.txt")"
  fi

  cat > "${SUMMARY_FILE}" <<EOT
started_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
backend_api=http://localhost
admin_url=${admin_url}
apk_download_url=${apk_url}
compose_file=${COMPOSE_FILE}
EOT
}

acquire_lock
cleanup_runtime_state

if [[ -x "${ROOT_DIR}/scripts/setup_master.sh" ]]; then
  "${ROOT_DIR}/scripts/setup_master.sh"
fi

compose_up_args=(up -d --remove-orphans)
if [[ "${REBUILD_ON_START}" == "1" ]]; then
  compose_up_args=(up -d --build --remove-orphans)
else
  compose_up_args=(up -d --no-build --remove-orphans)
fi

if [[ "${ENABLE_NGINX}" == "1" ]]; then
  docker compose -f "${COMPOSE_FILE}" "${compose_up_args[@]}"
else
  mapfile -t compose_services < <(docker compose -f "${COMPOSE_FILE}" config --services | grep -v '^nginx$')
  docker compose -f "${COMPOSE_FILE}" "${compose_up_args[@]}" "${compose_services[@]}"
fi

log "Backend iniciado via docker compose."

if [[ "${SEED_ON_START}" == "1" && -f "${SEED_FILE}" ]]; then
  if docker ps --format '{{.Names}}' | grep -q '^nexora-postgres$'; then
    wait_for_postgres_ready
    log "Aplicando seed mestre..."
    docker exec -i nexora-postgres psql -v ON_ERROR_STOP=1 -U nexora -d nexora_pay < "${SEED_FILE}" >/dev/null
    log "Seed aplicada."
  else
    log "Aviso: container nexora-postgres não encontrado, seed ignorada."
  fi
fi

healthcheck_stack
start_admin_panel
start_apk_download_server
write_summary

cat <<'EOT'
[start_nexora] Stack pronta.
- Backend/API: via docker compose
- Admin God Mode: http://localhost:5173 (ou ADMIN_MODE=skip)
- Download APK local: runtime/apk-download-url.txt
- Resumo operacional: runtime/start-summary.txt
- Flutter Super App (execução manual):
  cd apps/super-app-flutter
  flutter run -d chrome --dart-define=NEXORA_API_BASE_URL=http://localhost
EOT
