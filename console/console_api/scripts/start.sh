#!/bin/bash
set -e

# ============================================================
# start.sh - Build and start console API service
# Usage: ./console/console_api/scripts/start.sh
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")"; pwd)"
MODULE_DIR="$(cd "$SCRIPT_DIR/.."; pwd)"
PROJECT_ROOT="$(cd "$MODULE_DIR/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"
LOG_DIR="$PROJECT_ROOT/.log"

# Load .env
if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$PROJECT_ROOT/.env"
  set +a
fi

CONSOLE_API_PORT="${CONSOLE_API_PORT:-8090}"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m'

# ----------------------------------------------------------
# Build
# ----------------------------------------------------------
echo -e "${CYAN}=== Build console ===${NC}"
bash "$SCRIPT_DIR/build.sh"
echo ""

# ----------------------------------------------------------
# Start
# ----------------------------------------------------------
mkdir -p "$LOG_DIR"

# Kill existing process on port
pids=$(lsof -ti :"$CONSOLE_API_PORT" 2>/dev/null || true)
if [[ -n "$pids" ]]; then
  echo -e "${YELLOW}Port $CONSOLE_API_PORT is occupied, terminating...${NC}"
  echo "$pids" | xargs kill -9 2>/dev/null || true
  sleep 1
fi

# Kill existing console binary
pids=$(pgrep -f "$BUILD_DIR/console" 2>/dev/null || true)
if [[ -n "$pids" ]]; then
  echo -e "${YELLOW}Detected old console process, terminating...${NC}"
  echo "$pids" | xargs kill -9 2>/dev/null || true
  sleep 1
fi

log_file="$LOG_DIR/console.log"
nohup "$BUILD_DIR/console" > "$log_file" 2>&1 &
pid=$!

echo -e "${GREEN}[console]${NC} starting (pid=$pid, port=$CONSOLE_API_PORT, log=$log_file)"

# Wait for ready
retries=0
while (( retries < 30 )); do
  if curl -fsS "http://127.0.0.1:${CONSOLE_API_PORT}/swagger/index.html" >/dev/null 2>&1; then
    echo -e "${GREEN}[console]${NC} ready (pid=$pid, port=$CONSOLE_API_PORT, log=$log_file)"
    exit 0
  fi
  retries=$((retries+1))
  sleep 1
done

echo -e "${RED}[console] startup failed${NC}"
if [[ -f "$log_file" ]]; then
  echo -e "${YELLOW}--- $log_file (tail -n 80) ---${NC}"
  tail -n 80 "$log_file" || true
fi
exit 1
