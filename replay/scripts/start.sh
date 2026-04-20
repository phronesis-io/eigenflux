#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

source .env 2>/dev/null || true

REPLAY_PORT="${REPLAY_PORT:-8092}"

# Kill existing process on port
PID=$(lsof -ti :"$REPLAY_PORT" 2>/dev/null || true)
if [ -n "$PID" ]; then
  kill "$PID" 2>/dev/null || true
  sleep 0.5
fi

mkdir -p .log
nohup ./build/replay > .log/replay.log 2>&1 &

echo "Replay service starting on :$REPLAY_PORT (PID $!)"
echo "Logs: .log/replay.log"
