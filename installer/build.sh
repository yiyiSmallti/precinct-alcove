#!/usr/bin/env bash
# Builds Alcove's installers: stages the runtime of each platform, then
# mygo build. Run on macOS (the .app, its signing and the .dmg need it; the
# Windows installer needs NSIS: brew install makensis).
#
#   installer/build.sh                       # darwin-arm64 darwin-amd64 windows-amd64
#   installer/build.sh darwin-arm64          # one platform
#   ALCOVE_RESTAGE=0 installer/build.sh      # keep runtimes already staged
#
# Signing, notarization and update signing are configured in mygo.json and
# the environment (see README.dev.md, 打包).
set -euo pipefail
cd "$(dirname "$0")/.."

plats=("$@")
[ ${#plats[@]} -eq 0 ] && plats=(darwin-arm64 darwin-amd64 windows-amd64)

targets=()
for p in "${plats[@]}"; do
  staged=resources/$p/runtime
  if [ "${ALCOVE_RESTAGE:-1}" != 0 ] || [ ! -d "$staged" ]; then
    installer/stage-runtime.sh "$p"
  fi
  targets+=("${p/-//}")
done
go tool mygo build -platform "$(IFS=,; echo "${targets[*]}")"
