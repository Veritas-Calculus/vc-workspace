#!/usr/bin/env bash
# Run INSIDE a disposable QEMU guest, never on a PVE host or the Mac.
# Shapes only outbound TCP/3389 to one test client. No SSH/QGA/API traffic.
set -euo pipefail

profile_args() {
  case "$1" in
    latency) printf '%s\n' 'delay 120ms 20ms distribution normal' ;;
    limited) printf '%s\n' 'delay 120ms 20ms distribution normal rate 6mbit' ;;
    loss) printf '%s\n' 'delay 120ms 20ms distribution normal loss random 1% rate 6mbit' ;;
    outage) printf '%s\n' 'loss 100%' ;;
    *) echo 'unknown profile' >&2; return 2 ;;
  esac
}

if [[ ${1:-} == --describe && $# == 2 ]]; then profile_args "$2"; exit; fi

restore() {
  # Never overwrite a qdisc installed by someone else after our run.
  if tc -j qdisc show dev "$1" | python3 -c '
import json, sys
rows = json.load(sys.stdin)
sys.exit(0 if any(r.get("root") and r.get("kind") == "prio" and r.get("handle") == "7c01:" for r in rows) else 1)
'; then
    # Removing our root restores the kernel's original mq/default leaves
    # (explicitly adding mq would allocate a different root handle).
    tc qdisc del dev "$1" root
  fi
}

if [[ ${1:-} == --restore && $# == 2 && $2 =~ ^[a-zA-Z0-9_-]{1,15}$ ]]; then
  restore "$2"
  exit
fi

[[ $# == 5 ]] || { echo 'usage: rdp-network-lab.sh <expected-hostname> <interface> <client-ipv4> <latency|limited|loss|outage> <5..120 seconds>' >&2; exit 2; }
expected_host=$1
interface=$2
client_ip=$3
profile=$4
duration=$5
[[ $EUID == 0 && ${VC_WORKSPACE_NETWORK_LAB_ACK:-} == isolated-guest ]] || { echo 'requires root and explicit isolated-guest acknowledgement' >&2; exit 2; }
[[ $(hostname) == "$expected_host" ]] || { echo 'not the expected guest' >&2; exit 2; }
case $(systemd-detect-virt) in qemu|kvm) ;; *) echo 'requires a QEMU/KVM guest' >&2; exit 2 ;; esac
[[ $interface =~ ^[a-zA-Z0-9_-]{1,15}$ && $duration =~ ^[0-9]{1,3}$ ]] || exit 2
(( 10#$duration >= 5 && 10#$duration <= 120 )) || exit 2
python3 -c 'import ipaddress,sys; a=ipaddress.IPv4Address(sys.argv[1]); sys.exit(a.is_multicast or a.is_unspecified or a.is_loopback)' "$client_ip"
read -r -a impairment <<< "$(profile_args "$profile")"
[[ ${#impairment[@]} -gt 0 ]] || exit 2
script=$(readlink -f -- "$0")
[[ $(stat -c %u "$script") == 0 && ! -L $0 ]] || exit 2
[[ -z $(find "$script" -maxdepth 0 -perm /022 -print) ]] || exit 2
exec 9>"/run/vcw-network-lab-${interface}.lock"
flock -n 9 || { echo 'another test owns this interface' >&2; exit 2; }

# Refuse custom networking. Removing our root recreates these default
# fq_codel leaves; don't pretend a generic qdisc JSON dump is restorable.
tc -j qdisc show dev "$interface" | python3 -c '
import json,sys
rows=json.load(sys.stdin)
roots=[r for r in rows if r.get("root")]
assert len(roots)==1 and roots[0]["kind"]=="mq" and roots[0]["handle"]=="0:" and not roots[0].get("options")
leaves=[r for r in rows if not r.get("root")]
assert leaves
expected={"limit":10240,"flows":1024,"quantum":1514,"target":4999,"interval":99999,"memory_limit":33554432,"ecn":True,"drop_batch":64}
assert all(r["kind"]=="fq_codel" and r["handle"]=="0:" and r.get("options")==expected for r in leaves)
'
unit="vcw-network-rollback-${interface}"
cleanup() {
  restore "$interface"
  systemctl stop "${unit}.timer" >/dev/null 2>&1 || true
}
# The independent timer still restores after SIGKILL or a lost QGA caller.
systemd-run --quiet --collect --unit="$unit" --on-active="$((10#$duration + 10))s" \
  /bin/bash "$script" --restore "$interface"
trap cleanup EXIT
trap 'exit 130' INT TERM HUP
tc qdisc replace dev "$interface" root handle 7c01: prio bands 3 priomap 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
tc qdisc add dev "$interface" parent 7c01:3 handle 7c02: netem limit 1000 "${impairment[@]}"
tc filter add dev "$interface" protocol ip parent 7c01: prio 1 u32 \
  match ip dst "$client_ip/32" match ip protocol 6 0xff match ip sport 3389 0xffff flowid 7c01:3
printf 'profile=%s duration=%ss direction=guest-to-client port=3389\n' "$profile" "$duration"
tc -s qdisc show dev "$interface"
sleep "$((10#$duration))" & wait $!
tc -s qdisc show dev "$interface"
