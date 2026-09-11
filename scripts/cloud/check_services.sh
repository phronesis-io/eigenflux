#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.."; pwd)"

if [[ -f "${PROJECT_ROOT}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${PROJECT_ROOT}/.env"
  set +a
fi

# shellcheck source=services.sh
source "${SCRIPT_DIR}/services.sh"

status=0

echo "==> systemd services"
for mod in "${ALL_MODULES[@]}"; do
  svc="$(module_to_unit "$mod")"
  if systemctl is-active --quiet "${svc}"; then
    echo "[OK]   ${svc}"
  else
    echo "[FAIL] ${svc}"
    status=1
  fi
done

echo ""
echo "==> port listeners"
for mod in "${ALL_MODULES[@]}"; do
  port="$(module_port "$mod")"
  [[ -z "$port" ]] && continue
  if ss -ltn | awk '{print $4}' | grep -Eq "[:.]${port}$"; then
    echo "[OK]   ${mod} listening on :${port}"
  else
    echo "[FAIL] ${mod} not listening on :${port}"
    status=1
  fi
done

echo ""
echo "==> etcd health"
if docker compose -f "${PROJECT_ROOT}/docker-compose.cloud.yml" ps etcd >/dev/null 2>&1; then
  if docker compose -f "${PROJECT_ROOT}/docker-compose.cloud.yml" exec -T etcd \
    etcdctl --endpoints=http://127.0.0.1:2379 endpoint health >/dev/null 2>&1; then
    echo "[OK]   etcd endpoint healthy"
  else
    echo "[FAIL] etcd endpoint unhealthy"
    status=1
  fi
else
  echo "[FAIL] etcd compose service not found"
  status=1
fi

echo ""
echo "==> quick endpoints"
if curl -fsS "http://127.0.0.1:${API_PORT:-8080}/api/v1/website/stats" >/dev/null 2>&1; then
  echo "[OK]   api /api/v1/website/stats"
else
  echo "[FAIL] api /api/v1/website/stats"
  status=1
fi

exit "${status}"
