#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
RUNTIME_DIR="${ROOT_DIR}/runtime"
STOP_DOCKER="${STOP_DOCKER:-1}"
NUKE_VOLUMES="${NUKE_VOLUMES:-0}"

mkdir -p "${RUNTIME_DIR}"

log() {
  echo "[stop_nexora] $*"
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
    log "Encerrando ${label} (pid ${pid})..."
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

  rm -f \
    "${RUNTIME_DIR}/apk-download-url.txt" \
    "${RUNTIME_DIR}/start-summary.txt" \
    "${RUNTIME_DIR}/start_nexora.lock"
}

stop_docker_stack() {
  if [[ "${STOP_DOCKER}" != "1" ]]; then
    log "STOP_DOCKER=${STOP_DOCKER}; stack Docker mantida em execução."
    return
  fi

  if [[ ! -f "${COMPOSE_FILE}" ]]; then
    log "Aviso: compose não encontrado em ${COMPOSE_FILE}; nada para derrubar."
    return
  fi

  if ! command -v docker >/dev/null 2>&1; then
    log "Aviso: docker não encontrado; limpeza restrita ao runtime local."
    return
  fi

  if ! docker info >/dev/null 2>&1; then
    log "Aviso: docker daemon indisponível; limpeza restrita ao runtime local."
    return
  fi

  local args=(down --remove-orphans)
  if [[ "${NUKE_VOLUMES}" == "1" ]]; then
    args+=(--volumes)
  fi

  log "Derrubando stack docker compose..."
  docker compose -f "${COMPOSE_FILE}" "${args[@]}"
}

cleanup_runtime_state
stop_docker_stack
log "Limpeza operacional concluída."
