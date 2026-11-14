#!/usr/bin/env bash
# 组织版整链验收，第一段（Charter 架构文档第 7 节第 8 条的组织那条）：跑 e2e 的 TestOrgChain。
# 真实的：Alcove 核心、daily 与 cloister 两个插件、Cloister（access、sync、bothost）、Gatehouse（org 模式）
# 与它的 gatehouse-cloister 插件。假的：模型提供方、企业微信、云沙盒编排。写出 e2e/report/org-chain.md。
#
# 用法：e2e/org.sh        要 DATABASE_URL（一个能 CREATE DATABASE 的 Postgres）、go、node ≥ 22.18、pnpm
#   DOVETAIL_DIR、CLOISTER_DIR、GATEHOUSE_DIR   三个仓库的检出，默认是本仓库旁边的 precinct-*；
#                                              precinct-cloister 里要先装好依赖（npm ci）
#   DOVETAIL_DAILY_TGZ、DOVETAIL_CLOISTER_TGZ   打好的两个插件包；不给就从 DOVETAIL_DIR 现打
#   ALCOVE_E2E_NODE、ALCOVE_E2E_DSH、ALCOVE_E2E_TOOLDIR
#                                              随包运行时；不给就用 resources/<平台>/runtime
#
# 启动顺序绕不开一个环：Cloister 起来时就得知道网关地址（它把地址连同令牌发给设备），而网关要等 Cloister
# 起来才知道它的地址和密钥。所以先挑定网关的端口告诉 Cloister，Cloister 起来后，测试再在那个端口上起网关。
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
W=$(mktemp -d)
CLOUD_PID=""
cleanup() {
  rc=$?
  if [ -n "$CLOUD_PID" ] && kill -0 "$CLOUD_PID" 2>/dev/null; then
    kill -TERM "$CLOUD_PID" 2>/dev/null || true
    wait "$CLOUD_PID" 2>/dev/null || true
  fi
  rm -rf "$W"
  exit "$rc"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# 调用者给的路径可以是相对的；下面会切目录，先按当前目录定成绝对路径。
abs() { case "$1" in /*) printf '%s' "$1" ;; *) printf '%s/%s' "$PWD" "$1" ;; esac; }
for v in DOVETAIL_DAILY_TGZ DOVETAIL_CLOISTER_TGZ ALCOVE_E2E_NODE ALCOVE_E2E_DSH ALCOVE_E2E_TOOLDIR; do
  if [ -n "${!v:-}" ]; then export "$v=$(abs "${!v}")"; fi
done

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) PLAT=darwin-arm64 ;;
  Darwin-x86_64) PLAT=darwin-amd64 ;;
  *) PLAT="" ;;
esac
RT="$ROOT/resources/$PLAT/runtime"
export ALCOVE_E2E_NODE="${ALCOVE_E2E_NODE:-$RT/node}"
export ALCOVE_E2E_DSH="${ALCOVE_E2E_DSH:-$RT/node_modules/@deepseek-ai/dsh/lib/bin.js}"
if [ -z "${ALCOVE_E2E_TOOLDIR:-}" ] && [ -d "$RT/bin" ]; then export ALCOVE_E2E_TOOLDIR="$RT/bin"; fi
[ -x "$ALCOVE_E2E_NODE" ] && [ -f "$ALCOVE_E2E_DSH" ] || fail "no runtime: set ALCOVE_E2E_NODE and ALCOVE_E2E_DSH, or stage one with installer/stage-runtime.sh"
[ -n "${DATABASE_URL:-}" ] || fail "DATABASE_URL is needed (a Postgres that may CREATE DATABASE)"

repo() { (cd "${1:-$ROOT/../$2}" 2>/dev/null && pwd) || fail "no $2 checkout: set $3"; }
DOVETAIL=$(repo "${DOVETAIL_DIR:-}" precinct-dovetail DOVETAIL_DIR)
CLOISTER=$(repo "${CLOISTER_DIR:-}" precinct-cloister CLOISTER_DIR)
GATEHOUSE=$(repo "${GATEHOUSE_DIR:-}" precinct-gatehouse GATEHOUSE_DIR)
[ -d "$CLOISTER/node_modules" ] || fail "precinct-cloister has no node_modules: run npm ci there first"

printf '\n== 打插件包、构建网关（%s）\n' "$DOVETAIL"
(cd "$DOVETAIL" && go build -o "$W/dovetail" ./registry/cli)
pack() { # pack <plugin folder name> -> path of the package
  "$DOVETAIL/plugins/alcove/$1/build.sh" >/dev/null
  "$W/dovetail" pack "$DOVETAIL/plugins/alcove/$1" -o "$W/out" >/dev/null
  ls "$W"/out/precinct-"$1"-*.tgz
}
export DOVETAIL_DAILY_TGZ="${DOVETAIL_DAILY_TGZ:-$(pack daily)}"
export DOVETAIL_CLOISTER_TGZ="${DOVETAIL_CLOISTER_TGZ:-$(pack cloister)}"
(cd "$GATEHOUSE" && go build -o "$W/gatehouse" ./cmd/gatehouse)
# 插件自带的 build.sh 只出 Linux 与 Windows 的；这里按本机平台现编一个。
(cd "$DOVETAIL" && go build -o "$W/gatehouse-cloister" ./plugins/gatehouse/cloister/sidecar)
export ALCOVE_E2E_GATEHOUSE="$W/gatehouse" ALCOVE_E2E_GATEHOUSE_CLOISTER="$W/gatehouse-cloister"

printf '\n== 编译 e2e\n'
(cd "$ROOT" && go test -tags e2e -c -o "$W/e2e.test" ./e2e/)

printf '\n== 起 Cloister（%s）\n' "$CLOISTER"
PORT=$("$ALCOVE_E2E_NODE" -e 's=require("net").createServer().listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})')
export ALCOVE_E2E_GATEWAY_LISTEN="127.0.0.1:$PORT"
(cd "$CLOISTER" && CLOISTER_E2E_GATEWAY_URL="http://127.0.0.1:$PORT" exec node e2e/serve.ts "$W/cloud.env") >"$W/cloud.log" 2>&1 &
CLOUD_PID=$!
for _ in $(seq 900); do
  [ -f "$W/cloud.env" ] && break
  kill -0 "$CLOUD_PID" 2>/dev/null || { tail -30 "$W/cloud.log" >&2; fail "the cloud did not start"; }
  sleep 0.1
done
[ -f "$W/cloud.env" ] || { tail -30 "$W/cloud.log" >&2; fail "the cloud did not come up in 90 s"; }
# shellcheck disable=SC1091
. "$W/cloud.env"

printf '\n== 跑整链（网关 %s）\n' "$ALCOVE_E2E_GATEWAY_LISTEN"
(cd "$ROOT/e2e" && "$W/e2e.test" -test.run '^TestOrgChain$' -test.count=1 -test.v)
