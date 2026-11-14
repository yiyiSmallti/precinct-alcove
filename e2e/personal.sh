#!/usr/bin/env bash
# 个人版整链验收（Charter 架构文档第 7 节第 8 条的个人那条）：跑 e2e 的 TestPersonal，
# 并抓下被测进程发出的全部流量，由测试判定是否只有模型端点。写出 e2e/report/personal.md，
# 抓到的包留在 e2e/report/personal.pcap。
#
# 用法：e2e/personal.sh
#   DOVETAIL_DAILY_TGZ   打好的 @precinct/daily；不给就从 DOVETAIL_DIR（默认 ../precinct-dovetail）现打
#   ALCOVE_E2E_NODE、ALCOVE_E2E_DSH、ALCOVE_E2E_TOOLDIR
#                        随包运行时；不给就用 resources/<平台>/runtime（installer/stage-runtime.sh 备出来的）
#   PERSONAL_CAPTURE=0   不抓包（报告里那一步记为 SKIP）
#
# 抓包只在 Linux 上做，要 root（sudo）、iproute2、tcpdump、socat：
#   测试进程放进一个新建的网络命名空间，它通往外面的唯一一条路是一对 veth；tcpdump 守在 veth 的宿主一端，
#   所以包里只有被测进程树的流量，没有这台机器上别的进程的。命名空间里的解析器指向宿主一端（那里没有
#   DNS 服务），于是任何域名查询都会被抓到，而且立刻失败。假模型在命名空间里监听，凭据里填的却是宿主一端
#   的地址，由 socat 转回来：模型流量因此也走这条被抓的路，包里看不到它就说明没抓上。
# 别的系统上没有等价的免配置办法，只跑链路、不抓包。
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
W=$(mktemp -d)
NS=""; HOSTIF=""; PIDS=()
cleanup() {
  rc=$?
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && sudo kill "$p" 2>/dev/null || true; done
  if [ -n "$NS" ]; then
    sudo ip netns del "$NS" 2>/dev/null || true
    sudo ip link del "$HOSTIF" 2>/dev/null || true
    sudo rm -rf "/etc/netns/$NS"
  fi
  [ -f "$W/personal.pcap" ] && mkdir -p "$ROOT/e2e/report" && cp "$W/personal.pcap" "$ROOT/e2e/report/personal.pcap"
  rm -rf "$W"
  exit "$rc"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

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

if [ -z "${DOVETAIL_DAILY_TGZ:-}" ]; then
  DOVETAIL=$(cd "${DOVETAIL_DIR:-$ROOT/../precinct-dovetail}" 2>/dev/null && pwd) || fail "no precinct-dovetail checkout: set DOVETAIL_DIR or DOVETAIL_DAILY_TGZ"
  printf '\n== 打 @precinct/daily（%s）\n' "$DOVETAIL"
  "$DOVETAIL/plugins/alcove/daily/build.sh" >/dev/null
  (cd "$DOVETAIL" && go build -o "$W/dovetail" ./registry/cli)
  "$W/dovetail" pack "$DOVETAIL/plugins/alcove/daily" -o "$W/out" >/dev/null
  DOVETAIL_DAILY_TGZ=$(ls "$W"/out/precinct-daily-*.tgz)
fi
export DOVETAIL_DAILY_TGZ

# 先在命名空间外面把测试编好：进去之后 go 工具链不该再有任何理由碰网络。
printf '\n== 编译 e2e\n'
(cd "$ROOT" && go test -tags e2e -c -o "$W/e2e.test" ./e2e/)
RUN=("$W/e2e.test" -test.run '^TestPersonal$' -test.count=1 -test.v)

if [ "${PERSONAL_CAPTURE:-1}" = 0 ] || [ "$(uname -s)" != Linux ]; then
  printf '\n== 跑整链（不抓包：%s）\n' "$([ "$(uname -s)" = Linux ] && echo 'PERSONAL_CAPTURE=0' || echo '只有 Linux 上抓')"
  (cd "$ROOT/e2e" && "${RUN[@]}")
  exit
fi

for tool in ip tcpdump socat; do command -v "$tool" >/dev/null || fail "$tool is needed for the capture"; done
sudo -n true 2>/dev/null || fail "the capture needs root (sudo without a password)"
HOSTIP=10.211.77.1; NSIP=10.211.77.2; RELAY_PORT=18080; MODEL_PORT=18081
ss -ltnH "sport = :$RELAY_PORT" | grep -q . && fail "port $RELAY_PORT is taken on this machine"
ME=$(id -un)

printf '\n== 建网络命名空间与抓包\n'
NS="alcove-e2e-$$"; HOSTIF="aeh$$"; NSIF="aen$$"
sudo ip netns add "$NS"
sudo ip link add "$HOSTIF" type veth peer name "$NSIF"
sudo ip link set "$NSIF" netns "$NS"
sudo ip addr add "$HOSTIP/30" dev "$HOSTIF"
sudo ip link set "$HOSTIF" up
sudo ip netns exec "$NS" ip addr add "$NSIP/30" dev "$NSIF"
sudo ip netns exec "$NS" ip link set "$NSIF" up
sudo ip netns exec "$NS" ip link set lo up
sudo ip netns exec "$NS" ip route add default via "$HOSTIP"
sudo mkdir -p "/etc/netns/$NS"
echo "nameserver $HOSTIP" | sudo tee "/etc/netns/$NS/resolv.conf" >/dev/null
MAC=$(sudo ip netns exec "$NS" cat "/sys/class/net/$NSIF/address")

# -U：每个包立刻写盘，测试结束时自己读这个文件；-Z：文件归当前用户，测试才读得到。
sudo tcpdump -i "$HOSTIF" -U -n -Z "$ME" -w "$W/personal.pcap" 2>"$W/tcpdump.err" & PIDS+=($!)
for _ in $(seq 50); do [ "$(stat -c %s "$W/personal.pcap" 2>/dev/null || echo 0)" -ge 24 ] && break; sleep 0.1; done
[ "$(stat -c %s "$W/personal.pcap" 2>/dev/null || echo 0)" -ge 24 ] || { cat "$W/tcpdump.err" >&2; fail "tcpdump did not start"; }
socat "TCP-LISTEN:$RELAY_PORT,bind=$HOSTIP,fork,reuseaddr" "TCP:$NSIP:$MODEL_PORT" & PIDS+=($!)

printf '\n== 跑整链（命名空间 %s，出口 %s，%s）\n' "$NS" "$HOSTIF" "$MAC"
sudo ip netns exec "$NS" sudo -u "$ME" env \
  PATH="$PATH" HOME="$HOME" \
  ALCOVE_E2E_NODE="$ALCOVE_E2E_NODE" ALCOVE_E2E_DSH="$ALCOVE_E2E_DSH" ALCOVE_E2E_TOOLDIR="${ALCOVE_E2E_TOOLDIR:-}" \
  DOVETAIL_DAILY_TGZ="$DOVETAIL_DAILY_TGZ" \
  ALCOVE_E2E_PCAP="$W/personal.pcap" ALCOVE_E2E_PCAP_MAC="$MAC" \
  ALCOVE_E2E_MODEL_LISTEN="$NSIP:$MODEL_PORT" ALCOVE_E2E_MODEL_URL="http://$HOSTIP:$RELAY_PORT" \
  bash -c 'cd "$1" && shift && exec "$@"' _ "$ROOT/e2e" "${RUN[@]}"
