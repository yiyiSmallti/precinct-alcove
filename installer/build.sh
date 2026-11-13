#!/usr/bin/env bash
# Builds Alcove's installers: stages the runtime of each platform, then
# mygo build. Run on macOS (the .app, its signing and the .dmg need it; the
# Windows installer needs NSIS: brew install makensis).
#
#   installer/build.sh                       # darwin-arm64 darwin-amd64 windows-amd64
#   installer/build.sh darwin-arm64          # one platform
#   ALCOVE_RESTAGE=0 installer/build.sh      # keep runtimes already staged (the bridge is always repacked)
#   ALCOVE_MARKET=0 installer/build.sh       # keep the staged plugin market
#   ALCOVE_SIGN="Apple Development: …" installer/build.sh
#                                            # sign with a fixed identity, not ad hoc
#
# An ad hoc signature names the app by its hash, so every build is a new
# app to the Keychain and asks for the login password again; signed with a
# certificate, rebuilds keep the access the user granted.
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
  else
    # The bridge is Alcove's own code, not part of the downloaded runtime:
    # a kept runtime still gets the bridge as it is now.
    tmp=$(mktemp -d)
    (cd bridge && npm pack --silent --pack-destination "$tmp" >/dev/null)
    mv "$tmp"/precinct-alcove-bridge-*.tgz "$staged/alcove-bridge.tgz"
    rm -rf "$tmp"
  fi
  targets+=("${p/-//}")
done
if [ "${ALCOVE_MARKET:-1}" != 0 ]; then
  installer/stage-market.sh "${plats[@]}"
fi
sign=()
[ -n "${ALCOVE_SIGN:-}" ] && sign=(-sign "$ALCOVE_SIGN")
go tool mygo build -platform "$(IFS=,; echo "${targets[*]}")" ${sign[@]+"${sign[@]}"}
