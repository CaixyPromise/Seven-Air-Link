#!/usr/bin/env bash
# Two distinct Linux network namespaces; no host interfaces/routes are changed.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "${SLB_INSIDE_TEST_NS:-}" != 1 ]]; then
  exec unshare -Urnm env SLB_INSIDE_TEST_NS=1 bash "$0" "$@"
fi
mount --make-rprivate /
mkdir -p /run/netns
mount -t tmpfs tmpfs /run/netns
cleanup() {
  for ns in slb-m4-a slb-m4-b; do
    ip netns pids "$ns" 2>/dev/null | xargs -r kill -TERM 2>/dev/null || true
  done
  sleep 0.2
  for ns in slb-m4-a slb-m4-b; do
    ip netns pids "$ns" 2>/dev/null | xargs -r kill -KILL 2>/dev/null || true
    ip netns del "$ns" 2>/dev/null || true
  done
}
trap cleanup EXIT
ip netns add slb-m4-a
ip netns add slb-m4-b
ip link add slb-va type veth peer name slb-vb
ip link set slb-va netns slb-m4-a
ip link set slb-vb netns slb-m4-b
ip -n slb-m4-a addr add 10.231.0.11/24 dev slb-va
ip -n slb-m4-b addr add 10.231.0.12/24 dev slb-vb
ip -n slb-m4-a link set lo up
ip -n slb-m4-b link set lo up
ip -n slb-m4-a link set slb-va up
ip -n slb-m4-b link set slb-vb up
nsenter --net=/run/netns/slb-m4-a python3 -u scripts/e2e_two_linux.py "$@"
