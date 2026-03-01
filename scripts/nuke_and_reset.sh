#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
NGINX_CERTS_DIR="${ROOT_DIR}/infra/nginx/certs"

usage() {
  cat <<EOF
Uso:
  ./scripts/nuke_and_reset.sh [--force]

Descrição:
  Faz limpeza completa da stack NEXORA (containers, volumes e rede do projeto),
  recria artefatos de infraestrutura (setup_master) e sobe novamente.
EOF
}

confirm() {
  local force="${1:-false}"
  if [[ "${force}" == "true" ]]; then
    return 0
  fi

  read -r -p "Esta ação vai derrubar e recriar a stack. Continuar? [y/N] " answer
  case "${answer}" in
    y|Y|yes|YES) return 0 ;;
    *) echo "Cancelado."; exit 0 ;;
  esac
}

main() {
  local force="false"
  if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
  fi
  if [[ "${1:-}" == "--force" ]]; then
    force="true"
  fi

  if [[ ! -f "${COMPOSE_FILE}" ]]; then
    echo "[nuke_and_reset] ERRO: docker-compose.yml não encontrado em ${ROOT_DIR}."
    exit 1
  fi

  confirm "${force}"

  echo "[nuke_and_reset] Derrubando stack..."
  (cd "${ROOT_DIR}" && docker compose down --volumes --remove-orphans)

  echo "[nuke_and_reset] Removendo rede dedicada (se existir)..."
  docker network rm nexora-net >/dev/null 2>&1 || true

  if [[ -d "${NGINX_CERTS_DIR}" ]]; then
    echo "[nuke_and_reset] Limpando certificados TLS..."
    rm -f "${NGINX_CERTS_DIR}"/*.crt "${NGINX_CERTS_DIR}"/*.key || true
  fi

  echo "[nuke_and_reset] Recriando infraestrutura base..."
  "${ROOT_DIR}/scripts/setup_master.sh"

  echo "[nuke_and_reset] Subindo stack do zero..."
  (cd "${ROOT_DIR}" && docker compose up -d --build)

  echo "[nuke_and_reset] Stack reinicializada com sucesso."
  (cd "${ROOT_DIR}" && docker compose ps)
}

main "$@"
