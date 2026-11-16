#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cargo build --release --manifest-path "$ROOT_DIR/Cargo.toml"
mkdir -p "$ROOT_DIR/bin"
cp "$ROOT_DIR/target/release/git-edit" "$ROOT_DIR/bin/git-edit"

echo "Built $ROOT_DIR/bin/git-edit"
echo "Put it on your PATH (or run 'cargo install --path .') to use it as 'git edit'."
