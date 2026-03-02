#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
ADMIN_DIR="${ROOT_DIR}/apps/admin-god-mode"
LOG_DIR="${ROOT_DIR}/logs"
RUNTIME_DIR="${ROOT_DIR}/runtime"
SEED_FILE="${ROOT_DIR}/db/sql/seed.sql"
SEED_ON_START="${SEED_ON_START:-1}"

mkdir -p "${LOG_DIR}" "${RUNTIME_DIR}"

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  echo "[start_nexora] ERRO: compose não encontrado em ${COMPOSE_FILE}."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "[start_nexora] ERRO: Docker não encontrado."
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "[start_nexora] ERRO: Docker daemon indisponível."
  exit 1
fi

if [[ -x "${ROOT_DIR}/scripts/setup_master.sh" ]]; then
  "${ROOT_DIR}/scripts/setup_master.sh"
fi

docker compose -f "${COMPOSE_FILE}" up -d

echo "[start_nexora] Backend iniciado via docker compose."

if [[ "${SEED_ON_START}" == "1" && -f "${SEED_FILE}" ]]; then
  if docker ps --format '{{.Names}}' | grep -q '^nexora-postgres$'; then
    echo "[start_nexora] Aplicando seed mestre..."
    docker exec -i nexora-postgres psql -U nexora -d nexora_pay < "${SEED_FILE}" >/dev/null
    echo "[start_nexora] Seed aplicada."
  else
    echo "[start_nexora] Aviso: container nexora-postgres não encontrado, seed ignorada."
  fi
fi

if [[ -d "${ADMIN_DIR}" ]]; then
  if command -v npm >/dev/null 2>&1; then
    if [[ ! -d "${ADMIN_DIR}/node_modules" ]]; then
      echo "[start_nexora] Instalando dependências do painel admin..."
      (cd "${ADMIN_DIR}" && npm install)
    fi

    if [[ -f "${RUNTIME_DIR}/admin-god-mode.pid" ]]; then
      old_pid="$(cat "${RUNTIME_DIR}/admin-god-mode.pid" 2>/dev/null || true)"
      if [[ -n "${old_pid}" ]] && ps -p "${old_pid}" >/dev/null 2>&1; then
        kill "${old_pid}" || true
      fi
    fi

    echo "[start_nexora] Subindo painel admin em http://localhost:5173 ..."
    (
      cd "${ADMIN_DIR}"
      nohup npm run dev -- --host 0.0.0.0 --port 5173 > "${LOG_DIR}/admin-god-mode.log" 2>&1 &
      echo $! > "${RUNTIME_DIR}/admin-god-mode.pid"
    )
  else
    echo "[start_nexora] Aviso: npm não encontrado. Painel admin não iniciado."
  fi
fi

cat <<'EOF'
[start_nexora] Stack pronta.
- Backend/API: via docker compose
- Admin God Mode: http://localhost:5173
- Flutter Super App (execução manual):
  cd apps/super-app-flutter
  flutter run -d chrome --dart-define=NEXORA_API_BASE_URL=http://localhost
EOF
