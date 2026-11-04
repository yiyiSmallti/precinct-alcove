#!/usr/bin/env bash
# Stages the plugins Alcove preinstalls into each platform's runtime:
# resources/<platform>/runtime/plugins/<package>.tgz and its .sig. They are
# Dovetail's @precinct/dovetail (the plugin market), @precinct/daily (the
# daily bot) and @precinct/cloister (organization features, needs daily).
# Builds them from a precinct-dovetail checkout and signs them with the
# first-party key, which only the release Mac has.
#
#   installer/stage-market.sh darwin-arm64 [platform...]
#
#   DOVETAIL_DIR            precinct-dovetail checkout (default ../precinct-dovetail)
#   PRECINCT_SIGNING_KEY    first-party private key (default ~/.precinct/keys/precinct-first-party.key)
#   DOVETAIL_DEFAULT_INDEX  the index the market reads until the user sets one
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)
dovetail=$(cd "${DOVETAIL_DIR:-$root/../precinct-dovetail}" && pwd)
key=${PRECINCT_SIGNING_KEY:-$HOME/.precinct/keys/precinct-first-party.key}
[ -f "$key" ] || { echo "stage-market: no signing key at $key (set PRECINCT_SIGNING_KEY)" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd "$dovetail" && go build -o "$tmp/dovetail" ./registry/cli)
for plugin in dovetail daily cloister; do
  "$dovetail/plugins/alcove/$plugin/build.sh"
  "$tmp/dovetail" pack "$dovetail/plugins/alcove/$plugin" -o "$tmp/out" >/dev/null
done
for tgz in "$tmp"/out/*.tgz; do
  "$tmp/dovetail" sign "$tgz" -key "$key" >/dev/null
done

for p in "$@"; do
  dest=resources/$p/runtime/plugins
  rm -rf "$dest" && mkdir -p "$dest"
  cp "$tmp"/out/*.tgz "$tmp"/out/*.tgz.sig "$dest/"
done
