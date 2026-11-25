#!/usr/bin/env bash
# Stages the runtime Alcove ships into resources/, where mygo build picks it
# up as runtime/ in the app's resources:
#
#   runtime/node[.exe]                       Node (official build)
#   runtime/node_modules/@deepseek-ai/dsh    dsh, locked version, and pnpm
#   runtime/bin/pnpm[.cmd]                   pnpm shim (bundled node + pnpm),
#                                            for dsh's plugin installs
#   runtime/alcove-bridge.tgz                the bridge bundle
#
# Usage: installer/stage-runtime.sh darwin-arm64|darwin-amd64|windows-amd64
#
# Each Mac CPU has its own .app and runtime, in resources/darwin-<cpu>: a
# universal one would carry the node and native packages (LibreOffice,
# sherpa-onnx, sharp, …) of both CPUs, some 320 MB more.
#
# Runs on macOS or Linux (curl, tar, unzip, npm). Native optional packages
# are installed for the target, not for the build machine.
set -euo pipefail

NODE_VERSION=${NODE_VERSION:-24.11.1}
DSH_VERSION=0.2.1-rc.5
PNPM_VERSION=10.28.0
# The zod dsh's packages pin exactly. Other packages take the newest 4.x,
# so npm would give each of some 50 dsh packages its own copy (310 MB);
# every range in the tree accepts this one.
ZOD_VERSION=4.4.3

plat=${1:?usage: stage-runtime.sh darwin-arm64|darwin-amd64|windows-amd64}
root=$(cd "$(dirname "$0")/.." && pwd)
cache=${ALCOVE_STAGE_CACHE:-$HOME/.cache/alcove-stage}
mkdir -p "$cache"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sha256check() { # dir file.sha256
  (cd "$1" && if command -v sha256sum >/dev/null; then sha256sum -c "$2"; else shasum -a 256 -c "$2"; fi) >/dev/null
}

# node <dist> <dest dir>: the Node executable of an official build, checked
# against the release's SHASUMS256.txt.
node_exe() {
  local dist=$1 dest=$2 dir
  if [ ! -f "$cache/$dist" ]; then
    curl -fsSL -o "$cache/$dist.part" "https://nodejs.org/dist/v$NODE_VERSION/$dist"
    mv "$cache/$dist.part" "$cache/$dist"
  fi
  curl -fsSL "https://nodejs.org/dist/v$NODE_VERSION/SHASUMS256.txt" | grep " $dist\$" > "$cache/$dist.sha256"
  sha256check "$cache" "$dist.sha256"
  mkdir -p "$dest" "$tmp/x"
  rm -rf "${tmp:?}/x/"*
  case $dist in
    *.zip)    unzip -q "$cache/$dist" -d "$tmp/x"; dir=$tmp/x/${dist%.zip};    cp "$dir/node.exe" "$dest/" ;;
    *.tar.gz) tar -xzf "$cache/$dist" -C "$tmp/x"; dir=$tmp/x/${dist%.tar.gz}; cp "$dir/bin/node" "$dest/" ;;
  esac
  cp "$dir/LICENSE" "$tmp/NODE-LICENSE"
}

# modules <os> <cpu> <dest dir>: dsh and pnpm for that OS and CPU.
modules() {
  mkdir -p "$3"
  cat > "$3/package.json" <<JSON
{ "name": "alcove-runtime", "private": true,
  "dependencies": { "@deepseek-ai/dsh": "$DSH_VERSION", "pnpm": "$PNPM_VERSION" },
  "overrides": { "zod": "$ZOD_VERSION" } }
JSON
  # Install scripts would run for the build machine (koffi and node-pty
  # build or pick its binaries); the packages ship prebuilt binaries for
  # every target, so skip them and do what matters for the target here.
  (cd "$3" && npm install --no-audit --no-fund --omit=dev --ignore-scripts --os="$1" --cpu="$2" >/dev/null)
  # dsh-subprocess-local's postinstall: node-pty's spawn-helper loses its
  # executable bit in the tarball.
  find "$3/node_modules" -path '*/node-pty/prebuilds/*/spawn-helper' -exec chmod 755 {} +
  # npm's records and shims are for the build machine; the runtime has its own.
  rm -rf "$3/package-lock.json" "$3/node_modules/.bin" "$3/node_modules/.package-lock.json"
  unhide "$3/node_modules"
  prune "$1" "$2" "$3/node_modules"
  untelemetry "$3/node_modules"
}

# untelemetry <node_modules>: dsh's telemetry, beyond the rows the bridge
# bundle disables. llm-deepseek sends a persistent anonymous user id
# ($DSH_HOME/.anonymous-user-id) with every official DeepSeek request and has
# no switch for it, so that header goes; then the packages whose only job is
# telemetry, feedback upload or extra request fields: OTLP export and
# OpenTelemetry, product analytics, message and session feedback, Session
# logs on model requests, the plugin package list. A dsh upgrade that moves
# the header fails the build here rather than shipping it.
untelemetry() {
  local nm=$1 f=$1/@deepseek-ai/dsh-llm-deepseek/lib/index.js
  local hdr='"x-deepseek-harness-user-id": this.dependencies.resolveUserId(),'
  [ "$(grep -cF "$hdr" "$f")" = 1 ] || { echo "untelemetry: user id header not found once in $f" >&2; exit 1; }
  grep -vF "$hdr" "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  grep -q 'x-deepseek-harness-user-id' "$f" && { echo "untelemetry: user id header left in $f" >&2; exit 1; }
  rm -rf "$nm/@opentelemetry" "$nm"/@deepseek-ai/dsh-otel "$nm"/@deepseek-ai/dsh-session-telemetry \
    "$nm"/@deepseek-ai/dsh-session-telemetry-otel "$nm"/@deepseek-ai/dsh-host-product-telemetry-otel \
    "$nm"/@deepseek-ai/dsh-client-product-analytics "$nm"/@deepseek-ai/dsh-command-feedback \
    "$nm"/@deepseek-ai/dsh-message-feedback "$nm"/@deepseek-ai/dsh-client-ui-message-feedback \
    "$nm"/@deepseek-ai/dsh-session-log-deepseek "$nm"/@deepseek-ai/dsh-plugin-package-inventory-deepseek
}

# prune <os> <cpu> <node_modules>: what the runtime never loads. Source maps,
# type declarations, READMEs and changelogs; prebuilt binaries of other
# platforms; the speech-to-text engine and the inspector, experimental dsh
# plugins outside the sdk profile Alcove boots; and LibreOffice Kit's engine
# (150 MB), which dsh's Office skill downloads, once the user agrees, the
# first time a task needs it.
prune() {
  local nm=$3
  find "$nm" -type f \( -name '*.map' -o -name '*.d.ts' -o -name '*.d.mts' -o -name '*.d.cts' \
    -o -iname 'README*' -o -iname 'CHANGELOG*' -o -iname 'HISTORY*' \) -delete
  find "$nm" -type d -path '*/prebuilds/*' -prune ! -name "$1-$2" -exec rm -rf {} +
  rm -rf "$nm"/sherpa-onnx-* "$nm/@deepseek-ai/dsh-experimental-inspector" "$nm"/@deepseek-ai/libreoffice-kit-*
}

# unhide <node_modules>: mygo build leaves out every file whose name starts
# with a dot, so a package that loads one (pi-ai imports
# dist/providers/data/.manifest.json) breaks in the app. Such files are
# renamed to "dot<name>" and their references in the package's scripts
# rewritten; hidden files nothing refers to are left behind as before.
unhide() {
  node - "$1" <<'JS'
const fs = require('fs'), path = require('path')
const root = process.argv[2]
const pkgs = []
for (const d of fs.readdirSync(root)) {
  if (d.startsWith('.')) continue
  if (d.startsWith('@')) for (const s of fs.readdirSync(path.join(root, d))) pkgs.push(path.join(root, d, s))
  else pkgs.push(path.join(root, d))
}
const walk = (dir, out) => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) { if (e.name !== 'node_modules') walk(p, out) } else if (e.isFile()) out.push(p)
  }
  return out
}
let n = 0
for (const pkg of pkgs) {
  const files = walk(pkg, [])
  const hidden = files.filter(f => path.basename(f).startsWith('.') && !f.split(path.sep).some(s => s === '.bin'))
  if (!hidden.length) continue
  const scripts = files.filter(f => /\.(c|m)?js$/.test(f))
  for (const h of hidden) {
    const name = path.basename(h)
    // "…/.name" or a bare ".name" in quotes (path.join(dir, '.name')).
    const pairs = []
    for (const q of ["'", '"', '`']) pairs.push(['/' + name + q, '/dot' + name + q], [q + name + q, q + 'dot' + name + q])
    const users = scripts.filter(f => { const s = fs.readFileSync(f, 'utf8'); return pairs.some(([a]) => s.includes(a)) })
    if (!users.length) continue
    fs.renameSync(h, path.join(path.dirname(h), 'dot' + name))
    for (const f of users) {
      let s = fs.readFileSync(f, 'utf8')
      for (const [a, b] of pairs) s = s.split(a).join(b)
      fs.writeFileSync(f, s)
    }
    console.log(`unhid ${path.relative(root, h)} (${users.length} script${users.length > 1 ? 's' : ''})`)
    n++
  }
}
if (!n) console.log('no hidden files in use')
JS
}

# common <dest dir> <os>: shims, the bridge and the licence.
common() {
  local out=$1
  mkdir -p "$out/bin"
  if [ "$2" = win32 ]; then
    printf '@"%%~dp0..\\node.exe" "%%~dp0..\\node_modules\\pnpm\\bin\\pnpm.cjs" %%*\r\n' > "$out/bin/pnpm.cmd"
  else
    cat > "$out/bin/pnpm" <<'SH'
#!/bin/sh
d=$(dirname "$0")
exec "$d/../node" "$d/../node_modules/pnpm/bin/pnpm.cjs" "$@"
SH
    chmod +x "$out/bin/pnpm"
  fi
  (cd "$root/bridge" && npm pack --silent --pack-destination "$tmp" >/dev/null)
  mv "$tmp"/precinct-alcove-bridge-*.tgz "$out/alcove-bridge.tgz"
  cp "$tmp/NODE-LICENSE" "$out/"
}

case $plat in
  windows-amd64)
    out=$root/resources/windows-amd64/runtime
    rm -rf "$out"
    node_exe "node-v$NODE_VERSION-win-x64.zip" "$out"
    modules win32 x64 "$out"
    common "$out" win32
    echo "staged $out ($(du -sh "$out" | cut -f1))"
    ;;
  darwin-arm64|darwin-amd64)
    cpu=${plat#darwin-}
    [ "$cpu" = amd64 ] && cpu=x64
    out=$root/resources/$plat/runtime
    # resources/darwin/runtime is the universal build's shared tree, which
    # mygo would add to the .app of either CPU.
    rm -rf "$out" "$root/resources/darwin/runtime"
    node_exe "node-v$NODE_VERSION-darwin-$cpu.tar.gz" "$out"
    modules darwin "$cpu" "$out"
    common "$out" darwin
    echo "staged $out ($(du -sh "$out" | cut -f1))"
    ;;
  *) echo "unsupported platform $plat (darwin-arm64, darwin-amd64 or windows-amd64)" >&2; exit 2 ;;
esac
