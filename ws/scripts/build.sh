#!/bin/bash
set -e
PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
BUILD_DIR="$PROJECT_ROOT/build"
mkdir -p "$BUILD_DIR"
cd "$PROJECT_ROOT"
echo "Compiling ws..."
go build -o "$BUILD_DIR/ws" ./ws/
echo "OK → build/ws"
