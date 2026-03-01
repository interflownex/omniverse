#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
NGINX_DIR="${ROOT_DIR}/infra/nginx"
CERTS_DIR="${NGINX_DIR}/certs"
MAX_MEMORY_MB=13312

calculate_configured_memory_mb() {
  local sum=0
  while IFS= read -r value; do
    value="${value//\"/}"
    if [[ "${value}" =~ ^([0-9]+)([mMgG])$ ]]; then
      local num="${BASH_REMATCH[1]}"
      local unit="${BASH_REMATCH[2]}"
      if [[ "${unit}" =~ [gG] ]]; then
        sum=$((sum + num * 1024))
      else
        sum=$((sum + num))
      fi
    fi
  done < <(grep -E '^[[:space:]]*mem_limit:[[:space:]]*[0-9]+[mMgG]' "${COMPOSE_FILE}" | awk '{print $2}')

  echo "${sum}"
}

generate_tls_certificate() {
  mkdir -p "${CERTS_DIR}"
  if [[ -f "${CERTS_DIR}/nexora.crt" && -f "${CERTS_DIR}/nexora.key" ]]; then
    echo "[setup_master] Certificado TLS já existe em ${CERTS_DIR}."
    return
  fi

  if ! command -v openssl >/dev/null 2>&1; then
    echo "[setup_master] ERRO: openssl não encontrado. Instale openssl para gerar certificado TLS."
    exit 1
  fi

  openssl req -x509 -nodes -days 365 \
    -newkey rsa:2048 \
    -keyout "${CERTS_DIR}/nexora.key" \
    -out "${CERTS_DIR}/nexora.crt" \
    -subj "/C=BR/ST=SaoPaulo/L=SaoPaulo/O=NEXORA/OU=Platform/CN=localhost" >/dev/null 2>&1

  chmod 600 "${CERTS_DIR}/nexora.key" || true
  chmod 644 "${CERTS_DIR}/nexora.crt" || true
  echo "[setup_master] Certificado TLS autoassinado gerado."
}

ensure_docker_network() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "[setup_master] ERRO: Docker não encontrado."
    exit 1
  fi

  if ! docker network inspect nexora-net >/dev/null 2>&1; then
    docker network create nexora-net >/dev/null
    echo "[setup_master] Rede Docker nexora-net criada."
  else
    echo "[setup_master] Rede Docker nexora-net já existe."
  fi
}

main() {
  if [[ ! -f "${COMPOSE_FILE}" ]]; then
    echo "[setup_master] ERRO: docker-compose.yml não encontrado em ${ROOT_DIR}."
    exit 1
  fi

  local configured_mem_mb
  configured_mem_mb="$(calculate_configured_memory_mb)"
  if (( configured_mem_mb > MAX_MEMORY_MB )); then
    echo "[setup_master] ERRO: memória configurada (${configured_mem_mb}MB) excede teto de ${MAX_MEMORY_MB}MB (13GB)."
    exit 1
  fi
  echo "[setup_master] Memória total configurada no compose: ${configured_mem_mb}MB (teto: ${MAX_MEMORY_MB}MB)."

  generate_tls_certificate
  ensure_docker_network

  echo "[setup_master] Proxy Nginx seguro pronto."
}

main "$@"
