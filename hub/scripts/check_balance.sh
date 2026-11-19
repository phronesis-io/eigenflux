#!/usr/bin/env bash
# Thin wrapper: run check_balance.py with whichever python/python3 exists, forwarding all args and the exit code.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if command -v python3 >/dev/null 2>&1; then
    PYTHON_BIN=python3
elif command -v python >/dev/null 2>&1; then
    PYTHON_BIN=python
else
    echo "ERROR: neither python3 nor python found on PATH" >&2
    exit 1
fi

exec "$PYTHON_BIN" "$SCRIPT_DIR/check_balance.py" "$@"
