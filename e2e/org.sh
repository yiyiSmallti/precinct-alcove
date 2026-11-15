#!/usr/bin/env bash
# 组织版整链验收，前两段（Charter 架构文档第 7 节第 8 条的组织那条）：跑 e2e 的 TestOrgChain。
# 真实的：Alcove 核心、daily 与 cloister 两个插件、Cloister（access、sync、bothost）、成员在云端的 bot
# （daily-tools 引擎，从 DOVETAIL_DIR 现打，跑在随包的 dsh 里）、Gatehouse（org 模式）与它的 gatehouse-cloister
# 插件。假的：两个模型提供方、企业微信、云沙盒编排。写出 e2e/report/org-chain.md。
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
# 赋值与 export 分开写：`export X=$(cmd)` 的退出码是 export 的，cmd 失败会被吞掉，留下一个空路径；
# 测试见到空的变量就跳过，整条验收会一步没跑却显示成功。
pack() { # pack <plugin folder name>：打包并把包的路径留在 PACKED 里
  "$DOVETAIL/plugins/alcove/$1/build.sh" >/dev/null || fail "building the $1 plugin failed"
  "$W/dovetail" pack "$DOVETAIL/plugins/alcove/$1" -o "$W/out" >/dev/null || fail "packing the $1 plugin failed"
  PACKED=$(ls "$W"/out/precinct-"$1"-*.tgz) || fail "no $1 package came out of the pack"
}
if [ -z "${DOVETAIL_DAILY_TGZ:-}" ]; then pack daily; DOVETAIL_DAILY_TGZ=$PACKED; fi
if [ -z "${DOVETAIL_CLOISTER_TGZ:-}" ]; then pack cloister; DOVETAIL_CLOISTER_TGZ=$PACKED; fi
for f in "$DOVETAIL_DAILY_TGZ" "$DOVETAIL_CLOISTER_TGZ"; do [ -f "$f" ] || fail "no plugin package at $f"; done
export DOVETAIL_DAILY_TGZ DOVETAIL_CLOISTER_TGZ
(cd "$GATEHOUSE" && go build -o "$W/gatehouse" ./cmd/gatehouse)
# 插件自带的 build.sh 只出 Linux 与 Windows 的；这里按本机平台现编一个。
(cd "$DOVETAIL" && go build -o "$W/gatehouse-cloister" ./plugins/gatehouse/cloister/sidecar)
export ALCOVE_E2E_GATEHOUSE="$W/gatehouse" ALCOVE_E2E_GATEHOUSE_CLOISTER="$W/gatehouse-cloister"

printf '\n== 编译 e2e\n'
(cd "$ROOT" && go test -tags e2e -c -o "$W/e2e.test" ./e2e/)

printf '\n== 起 Cloister（%s）\n' "$CLOISTER"
PORT=$("$ALCOVE_E2E_NODE" -e 's=require("net").createServer().listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})')
export ALCOVE_E2E_GATEWAY_LISTEN="127.0.0.1:$PORT"
# 云端的 bot 用随包的 dsh：ALCOVE_E2E_DSH 是 <node_modules>/@deepseek-ai/dsh/lib/bin.js。
DSH_MODULES=$(cd "$(dirname "$ALCOVE_E2E_DSH")/../../.." && pwd)
# 云端的引擎闲 10 秒就退：本机设的提醒在云端到点时没有引擎在跑，云端自己发出（引擎还没听说），切回本机时要先交给引擎。
(cd "$CLOISTER" && CLOISTER_E2E_GATEWAY_URL="http://127.0.0.1:$PORT" CLOISTER_E2E_ENGINE=1 CLOISTER_E2E_DAILY_IDLE_S=10 DOVETAIL_DIR="$DOVETAIL" DSH_NODE_MODULES="$DSH_MODULES" \
  exec node e2e/serve.ts "$W/cloud.env") >"$W/cloud.log" 2>&1 &
CLOUD_PID=$!
# 先要打出 daily 包、装进 bot 的 daily profile，一两分钟。
for _ in $(seq 3000); do
  [ -f "$W/cloud.env" ] && break
  kill -0 "$CLOUD_PID" 2>/dev/null || { tail -30 "$W/cloud.log" >&2; fail "the cloud did not start"; }
  sleep 0.1
done
[ -f "$W/cloud.env" ] || { tail -30 "$W/cloud.log" >&2; fail "the cloud did not come up in 5 minutes"; }
# shellcheck disable=SC1091
. "$W/cloud.env"

printf '\n== 跑整链（网关 %s）\n' "$ALCOVE_E2E_GATEWAY_LISTEN"
# 失败时也要往下走：pipefail 加 set -e 会在这一行直接退出，云端的日志就打不出来了。
(cd "$ROOT/e2e" && "$W/e2e.test" -test.run '^TestOrgChain$' -test.count=1 -test.v) 2>&1 | tee "$W/test.log" || true
# 测试自己会在缺东西时跳过，跳过的退出码也是 0：这里只认它真的跑过并通过。
grep -q -- '^--- PASS: TestOrgChain' "$W/test.log" || { tail -150 "$W/cloud.log" >&2; fail "TestOrgChain did not run to a pass (skipped or not run)"; }
