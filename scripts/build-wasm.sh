#!/bin/sh
# Builds the restic reader for the browser into web/wasm/, where
# web/build.mjs picks it up.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$root/web/wasm"
cd "$root"
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/wasm/restic.wasm ./cmd/restash-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm/wasm_exec.js
echo "built web/wasm/restic.wasm"
