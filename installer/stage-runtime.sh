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
# Usage: installer/stage-runtime.sh darwin-universal|windows-amd64
#
# darwin-universal is one runtime for both Mac CPUs, as the universal .app
# needs: node goes to resources/darwin-arm64 and resources/darwin-amd64
# (mygo build joins them into a universal binary), and node_modules, with
# the native packages of both CPUs side by side (node-pty, sharp, …), to
# resources/darwin.
#
# Runs on macOS or Linux (curl, tar, unzip, npm). Native optional packages
# are installed for the target, not for the build machine.
set -euo pipefail

NODE_VERSION=${NODE_VERSION:-24.11.1}
DSH_VERSION=0.2.1-rc.5
PNPM_VERSION=10.28.0

plat=${1:?usage: stage-runtime.sh darwin-universal|windows-amd64}
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
  "dependencies": { "@deepseek-ai/dsh": "$DSH_VERSION", "pnpm": "$PNPM_VERSION" } }
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
node=$d/../node
# go run from the staged resources/: node is per CPU, next to darwin/.
if [ ! -x "$node" ]; then
  case $(uname -m) in arm64) cpu=arm64 ;; *) cpu=amd64 ;; esac
  node=$d/../../../darwin-$cpu/runtime/node
fi
exec "$node" "$d/../node_modules/pnpm/bin/pnpm.cjs" "$@"
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
  darwin-universal)
    shared=$root/resources/darwin/runtime
    rm -rf "$shared" "$root/resources/darwin-arm64/runtime" "$root/resources/darwin-amd64/runtime"
    node_exe "node-v$NODE_VERSION-darwin-arm64.tar.gz" "$root/resources/darwin-arm64/runtime"
    node_exe "node-v$NODE_VERSION-darwin-x64.tar.gz" "$root/resources/darwin-amd64/runtime"
    modules darwin arm64 "$tmp/arm64"
    modules darwin x64 "$tmp/x64"
    # One tree for both CPUs: the x64-only packages join the arm64 tree.
    # Anything both have must be the same file, or the CPUs would differ.
    (cd "$tmp/x64" && find node_modules -type f) | while read -r f; do
      if [ -e "$tmp/arm64/$f" ]; then
        cmp -s "$tmp/x64/$f" "$tmp/arm64/$f" || { echo "differs between arm64 and x64: $f" >&2; exit 1; }
      else
        mkdir -p "$(dirname "$tmp/arm64/$f")" && cp -p "$tmp/x64/$f" "$tmp/arm64/$f"
      fi
    done
    mkdir -p "$shared"
    mv "$tmp/arm64/node_modules" "$shared/"
    common "$shared" darwin
    echo "staged $shared ($(du -sh "$shared" | cut -f1)) and node for arm64 and amd64"
    ;;
  *) echo "unsupported platform $plat (darwin-universal or windows-amd64)" >&2; exit 2 ;;
esac
