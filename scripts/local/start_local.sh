#!/bin/bash
set -e

# ============================================================
# start_local.sh - Start all dependency services and microservices (one-click startup)
# Usage: ./scripts/local/start_local.sh [service_name]
#   No arguments: Start Docker dependencies + all microservices
#   Specify service: ./scripts/local/start_local.sh api
# ============================================================

PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"
LOG_DIR="$PROJECT_ROOT/.log"

# Load .env (for reading embedding and port configuration)
if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$PROJECT_ROOT/.env"
  set +a
fi

API_PORT="${API_PORT:-8080}"
CONSOLE_API_PORT="${CONSOLE_API_PORT:-8090}"
PROFILE_RPC_PORT="${PROFILE_RPC_PORT:-8881}"
ITEM_RPC_PORT="${ITEM_RPC_PORT:-8882}"
SORT_RPC_PORT="${SORT_RPC_PORT:-8883}"
FEED_RPC_PORT="${FEED_RPC_PORT:-8884}"
AUTH_RPC_PORT="${AUTH_RPC_PORT:-8886}"

ETCD_PORT="${ETCD_PORT:-2379}"
ELASTICSEARCH_HTTP_PORT="${ELASTICSEARCH_HTTP_PORT:-9200}"
KIBANA_PORT="${KIBANA_PORT:-5601}"
PROJECT_NAME="${PROJECT_NAME:-myhub}"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m'

dc() {
  (
    cd "$PROJECT_ROOT"
    docker compose -p "$PROJECT_NAME" "$@"
  )
}

is_service_running() {
  local service=$1
  dc ps --status running --services 2>/dev/null | grep -qx "$service"
}

# ----------------------------------------------------------
# Start Docker dependency services
# ----------------------------------------------------------
start_docker_services() {
  echo -e "${CYAN}=== Starting Docker dependency services ===${NC}"
  echo -e "  Compose Project: ${CYAN}${PROJECT_NAME}${NC}"

  local services="postgres redis etcd elasticsearch kibana"

  dc up -d $services

  # Wait for services to be ready
  echo -e "  Waiting for services to be ready..."

  # PostgreSQL
  local retries=0
  while ! dc exec -T postgres pg_isready -U eigenflux -d eigenflux > /dev/null 2>&1; do
    retries=$((retries+1))
    [[ $retries -gt 30 ]] && echo -e "${RED}✗ PostgreSQL startup timeout${NC}" && exit 1
    sleep 1
  done
  echo -e "  ${GREEN}✓ PostgreSQL ready${NC}"

  # Redis
  retries=0
  while ! dc exec -T redis redis-cli ping > /dev/null 2>&1; do
    retries=$((retries+1))
    [[ $retries -gt 30 ]] && echo -e "${RED}✗ Redis startup timeout${NC}" && exit 1
    sleep 1
  done
  echo -e "  ${GREEN}✓ Redis ready${NC}"

  # etcd
  retries=0
  while ! curl -s "http://localhost:${ETCD_PORT}/health" > /dev/null 2>&1; do
    retries=$((retries+1))
    [[ $retries -gt 30 ]] && echo -e "${RED}✗ etcd startup timeout${NC}" && exit 1
    sleep 1
  done
  echo -e "  ${GREEN}✓ etcd ready${NC}"

  # Elasticsearch
  retries=0
  while ! curl -s "http://localhost:${ELASTICSEARCH_HTTP_PORT}/_cluster/health" > /dev/null 2>&1; do
    retries=$((retries+1))
    [[ $retries -gt 60 ]] && echo -e "${RED}✗ Elasticsearch startup timeout${NC}" && exit 1
    sleep 2
  done
  echo -e "  ${GREEN}✓ Elasticsearch ready${NC}"

  # Kibana
  retries=0
  while ! curl -s "http://localhost:${KIBANA_PORT}/api/status" > /dev/null 2>&1; do
    retries=$((retries+1))
    [[ $retries -gt 60 ]] && echo -e "${RED}✗ Kibana startup timeout${NC}" && exit 1
    sleep 2
  done
  echo -e "  ${GREEN}✓ Kibana ready${NC}"

  echo ""
}

# ----------------------------------------------------------
# Check dependency services (check only, do not start)
# ----------------------------------------------------------
check_dependencies() {
  echo -e "${CYAN}=== Checking dependency services ===${NC}"
  echo -e "Compose Project: ${CYAN}${PROJECT_NAME}${NC}"

  local all_ok=true

  if ! curl -s "http://localhost:${ELASTICSEARCH_HTTP_PORT}/_cluster/health" > /dev/null 2>&1; then
    echo -e "${RED}✗ Elasticsearch not running${NC}"
    all_ok=false
  else
    echo -e "${GREEN}✓ Elasticsearch running${NC}"
  fi

  if ! curl -s "http://localhost:${KIBANA_PORT}/api/status" > /dev/null 2>&1; then
    echo -e "${RED}✗ Kibana not running${NC}"
    all_ok=false
  else
    echo -e "${GREEN}✓ Kibana running${NC}"
  fi

  if ! is_service_running "postgres"; then
    echo -e "${RED}✗ PostgreSQL not running${NC}"
    all_ok=false
  else
    echo -e "${GREEN}✓ PostgreSQL running${NC}"
  fi

  if ! is_service_running "redis"; then
    echo -e "${RED}✗ Redis not running${NC}"
    all_ok=false
  else
    echo -e "${GREEN}✓ Redis running${NC}"
  fi

  if ! is_service_running "etcd"; then
    echo -e "${RED}✗ etcd not running${NC}"
    all_ok=false
  else
    echo -e "${GREEN}✓ etcd running${NC}"
  fi

  echo ""
  [[ "$all_ok" == "true" ]]
}

# name:port mapping
SERVICE_MAP=(
  "profile:${PROFILE_RPC_PORT}"
  "item:${ITEM_RPC_PORT}"
  "sort:${SORT_RPC_PORT}"
  "feed:${FEED_RPC_PORT}"
  "auth:${AUTH_RPC_PORT}"
  "api:${API_PORT}"
  "console:${CONSOLE_API_PORT}"
  "pipeline:"
  "cron:"
)

wait_for_tcp_port() {
  local port=$1
  local retries=${2:-30}
  local attempt=0
  while (( attempt < retries )); do
    if lsof -iTCP:"$port" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
      return 0
    fi
    attempt=$((attempt+1))
    sleep 1
  done
  return 1
}

wait_for_http_ready() {
  local url=$1
  local retries=${2:-30}
  local attempt=0
  while (( attempt < retries )); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    attempt=$((attempt+1))
    sleep 1
  done
  return 1
}

print_api_share_message() {
  local log_file="$LOG_DIR/api.log"
  local line=""
  local retries=0

  while (( retries < 10 )); do
    if [[ -f "$log_file" ]]; then
      line=$(grep -F "Share this with your friends:" "$log_file" | tail -n 1 || true)
      if [[ -n "$line" ]]; then
        break
      fi
    fi
    retries=$((retries+1))
    sleep 1
  done

  if [[ -n "$line" ]]; then
    echo -e "${CYAN}${line}${NC}"
  fi
}

print_service_logs() {
  local name=$1
  local log_file="$LOG_DIR/${name}.log"
  local app_log_dir=""

  case "$name" in
    api)
      app_log_dir="$PROJECT_ROOT/api/.log"
      ;;
    console)
      app_log_dir="$PROJECT_ROOT/console/api/.log"
      ;;
    pipeline)
      app_log_dir="$PROJECT_ROOT/pipeline/.log"
      ;;
    cron)
      app_log_dir="$PROJECT_ROOT/pipeline/cron/.log"
      ;;
    *)
      app_log_dir="$PROJECT_ROOT/rpc/${name}/.log"
      ;;
  esac

  echo -e "${RED}[$name] startup failed${NC}"

  if [[ -f "$log_file" ]]; then
    echo -e "${YELLOW}--- $log_file (tail -n 80) ---${NC}"
    tail -n 80 "$log_file" || true
  fi

  if [[ -d "$app_log_dir" ]]; then
    local latest_app_log
    latest_app_log=$(ls -t "$app_log_dir"/*.log 2>/dev/null | head -n 1 || true)
    if [[ -n "$latest_app_log" ]]; then
      echo -e "${YELLOW}--- $latest_app_log (tail -n 80) ---${NC}"
      tail -n 80 "$latest_app_log" || true
    fi
  fi
}

wait_for_service_ready() {
  local name=$1
  local pid=$2
  local port=$3

  case "$name" in
    api)
      wait_for_http_ready "http://127.0.0.1:${port}/skill.md" 30
      ;;
    console)
      wait_for_http_ready "http://127.0.0.1:${port}/swagger/index.html" 30
      ;;
    pipeline|cron)
      sleep 2
      kill -0 "$pid" >/dev/null 2>&1
      ;;
    *)
      wait_for_tcp_port "$port" 30
      ;;
  esac
}

get_port() {
  local target=$1
  for entry in "${SERVICE_MAP[@]}"; do
    IFS=':' read -r name port <<< "$entry"
    if [[ "$name" == "$target" ]]; then
      echo "$port"
      return 0
    fi
  done
  return 1
}

kill_port() {
  local port=$1
  [[ -z "$port" ]] && return 0
  local pids
  pids=$(lsof -ti :"$port" 2>/dev/null || true)
  [[ -z "$pids" ]] && return 0
  echo -e "${YELLOW}Port $port is occupied, terminating...${NC}"
  echo "$pids" | xargs kill -9 2>/dev/null || true
  sleep 1
}

start_service() {
  local name=$1
  local port
  port=$(get_port "$name") || {
    echo -e "${RED}Unknown service: $name${NC}"
    echo "Available services: profile item sort feed auth api console pipeline cron"
    exit 1
  }

  kill_port "$port"
  local pids
  pids=$(pgrep -f "$BUILD_DIR/$name" 2>/dev/null || true)
  if [[ -n "$pids" ]]; then
    echo -e "${YELLOW}Detected old ${name} process, terminating...${NC}"
    echo "$pids" | xargs kill -9 2>/dev/null || true
    sleep 1
  fi

  local log_file="$LOG_DIR/${name}.log"
  nohup "$BUILD_DIR/$name" > "$log_file" 2>&1 &
  local pid=$!

  if [[ -n "$port" ]]; then
    echo -e "${GREEN}[$name]${NC} starting (pid=$pid, port=$port, log=$log_file)"
  else
    echo -e "${GREEN}[$name]${NC} starting (pid=$pid, log=$log_file)"
  fi

  if ! wait_for_service_ready "$name" "$pid" "$port"; then
    print_service_logs "$name"
    exit 1
  fi

  if [[ -n "$port" ]]; then
    echo -e "${GREEN}[$name]${NC} ready (pid=$pid, port=$port, log=$log_file)"
  else
    echo -e "${GREEN}[$name]${NC} ready (pid=$pid, log=$log_file)"
  fi
}

# ----------------------------------------------------------
# Main flow: start Docker dependencies only for a full startup
# ----------------------------------------------------------
if [[ -z "$1" ]]; then
  # Full startup: make sure Docker services are running first.
  if ! check_dependencies 2>/dev/null; then
    start_docker_services
  else
    echo -e "${GREEN}All dependency services are ready${NC}\n"
  fi
fi

# ----------------------------------------------------------
# Database migration
# ----------------------------------------------------------
if [[ "${SKIP_MIGRATE:-0}" != "1" ]]; then
  echo -e "${CYAN}=== Database migration ===${NC}"
  bash "$PROJECT_ROOT/scripts/common/migrate_up.sh"
  echo ""
fi

# ----------------------------------------------------------
# Build
# ----------------------------------------------------------
echo -e "${CYAN}=== Build ===${NC}"
if [[ -n "$1" ]]; then
  bash "$PROJECT_ROOT/scripts/common/build.sh" "$1"
else
  bash "$PROJECT_ROOT/scripts/common/build.sh"
fi
echo ""

# ----------------------------------------------------------
# Start microservices
# ----------------------------------------------------------
mkdir -p "$LOG_DIR"

echo -e "${CYAN}=== Start microservices ===${NC}"
if [[ -n "$1" ]]; then
  start_service "$1"
else
  for entry in "${SERVICE_MAP[@]}"; do
    IFS=':' read -r name _ <<< "$entry"
    start_service "$name"
  done
fi

echo -e "\n${GREEN}Done!${NC} View logs with: tail -f .log/<service>.log"

if wait_for_http_ready "http://127.0.0.1:${API_PORT}/skill.md" 1; then
  print_api_share_message
fi
