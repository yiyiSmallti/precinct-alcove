#!/usr/bin/env bash
# Builds the Windows installer on a Mac whose makensis does not work
# (Homebrew's makensis 3.13 crashes even on an empty script on the dev Mac):
# the runtime and the preinstalled plugins are staged here, where the
# first-party signing key is, and only `mygo build` runs in a Linux
# container with NSIS. Needs a docker that runs Linux containers (a colima
# VM is enough; arm64 is fine, the target is cross-compiled) and mounts this
# checkout and the Go module cache.
#
#   installer/build-windows.sh               # stage, then build in the container
#   ALCOVE_RESTAGE=0 installer/build-windows.sh   # keep the staged runtime
#   ALCOVE_MARKET=0 installer/build-windows.sh    # keep the staged plugins
#
# Output: build/windows-amd64/alcove-setup-<version>-amd64.exe (unsigned
# until mygo.json has windows.certificate).
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)

if [ "${ALCOVE_RESTAGE:-1}" != 0 ] || [ ! -d resources/windows-amd64/runtime ]; then
  installer/stage-runtime.sh windows-amd64
fi
if [ "${ALCOVE_MARKET:-1}" != 0 ]; then
  installer/stage-market.sh windows-amd64
fi

go_version=$(awk '/^go /{print $2}' go.mod)
docker run --rm -v "$root:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src \
  -e GOTOOLCHAIN=local "golang:$go_version" bash -c '
    apt-get update -qq && apt-get install -y -qq nsis >/dev/null
    git config --global --add safe.directory /src
    go tool mygo build -platform windows/amd64'
ls -la build/windows-amd64
