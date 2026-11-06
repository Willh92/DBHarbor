#!/usr/bin/env sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

OS_NAME="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_NAME="$(uname -m)"
case "$ARCH_NAME" in
  x86_64|amd64) GO_ARCH="amd64" ;;
  aarch64|arm64) GO_ARCH="arm64" ;;
  *) GO_ARCH="$ARCH_NAME" ;;
esac

BIN_PATH="$ROOT_DIR/dist/dbharbor-${OS_NAME}-${GO_ARCH}"
if [ ! -x "$BIN_PATH" ]; then
  echo "[start] Binary not found at $BIN_PATH, building now..."
  node scripts/build.mjs
fi

exec "$BIN_PATH" "$@"
