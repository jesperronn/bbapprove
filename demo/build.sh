#!/usr/bin/env bash
# shellcheck shell=bash
# build — build bbapprove for the demo recordings with a fixed version, so the footer and the help screenshot
# neither change with every commit nor say "-dirty". Prints the version it built.
#
# Usage: demo/build.sh        (run by bin/demo record, shots and regenerate)
# Env:   DEMO_VERSION   the version to embed (default: the newest release tag, else "dev")
# Exit:  0 built, 1 the build failed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
version="${DEMO_VERSION:-$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo dev)}"
BBAPPROVE_VERSION="$version" "$ROOT/bin/build" >/dev/null || {
  echo "build: bin/build failed; run bin/build --test to see why" >&2
  exit 1
}
echo "$version"
