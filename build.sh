#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -p "$ROOT_DIR/bin"
go build -o "$ROOT_DIR/bin/nexus" "$ROOT_DIR"

echo "Built $ROOT_DIR/bin/nexus"

