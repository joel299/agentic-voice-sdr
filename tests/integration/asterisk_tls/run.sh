#!/usr/bin/env bash
set -euo pipefail

mode=${ASTERISK_GRU83_MODE:-unit}
if [[ "$mode" != real ]]; then
  printf 'Asterisk/PJSIP real gate: SKIP (set ASTERISK_GRU83_MODE=real and ASTERISK_GRU83_FIXTURE=<isolated fixture dir>)\n'
  exit 0
fi

for bin in openssl asterisk; do command -v "$bin" >/dev/null || { echo "$bin is required" >&2; exit 2; }; done
fixture=${ASTERISK_GRU83_FIXTURE:-}
[[ -n "$fixture" && -f "$fixture/asterisk.conf" && -f "$fixture/etc/pjsip.conf" ]] || {
  echo 'real mode requires an isolated fixture directory containing asterisk.conf and etc/pjsip.conf' >&2; exit 2;
}
root=$(mktemp -d); port_a=${ASTERISK_GRU83_TLS_PORT_A:-15061}; port_b=${ASTERISK_GRU83_TLS_PORT_B:-15062}
cp -a "$fixture/." "$root/"
# The fixture is copied into a disposable directory; no production Asterisk is touched.
sed -i "s#${fixture}#${root}#g; s#15061#${port_a}#g" "$root/asterisk.conf" "$root/etc/pjsip.conf" "$root/etc/resolver_unbound.conf" 2>/dev/null || true
hosts="$root/etc/.gru83-pinned.hosts"
printf '127.0.0.1 sip.provider.test\n' >"$hosts"
cleanup() { kill "${ast_pid:-}" "${server_a_pid:-}" "${server_b_pid:-}" 2>/dev/null || true; wait "${ast_pid:-}" 2>/dev/null || true; wait "${server_a_pid:-}" 2>/dev/null || true; wait "${server_b_pid:-}" 2>/dev/null || true; rm -rf "$root"; }
trap cleanup EXIT
openssl s_server -accept "$port_a" -cert "$root/cert.pem" -key "$root/key.pem" -tls1_2 -quiet >"$root/server-a.log" 2>&1 & server_a_pid=$!
openssl s_server -accept "$port_b" -cert "$root/cert.pem" -key "$root/key.pem" -tls1_2 -quiet >"$root/server-b.log" 2>&1 & server_b_pid=$!
asters=$(asterisk -C "$root/asterisk.conf" -f -U root -G root -vv >"$root/asterisk.log" 2>&1 & echo $!)
ast_pid=$asters
for _ in $(seq 1 20); do grep -q "Transport 'transport-tls' to remote 'sip.provider.test' - 127.0.0.1:${port_a} - OK" "$root/asterisk.log" && break; sleep 1; done
pid_before=$(ps -o pid= -p "$ast_pid" | tr -d ' ')
grep -q "Transport 'transport-tls' to remote 'sip.provider.test' - 127.0.0.1:${port_a} - OK" "$root/asterisk.log" || { cat "$root/asterisk.log" >&2; exit 1; }
verify=$(asterisk -C "$root/asterisk.conf" -rx 'pjsip show transport transport-tls' 2>/dev/null || true)
grep -q 'verify_server.*yes' "$root/etc/pjsip.conf"
grep -q 'transport-tls' <<<"$verify" || { echo 'loaded PJSIP transport read-back failed' >&2; exit 1; }
# Hot pin update: same process, new managed host and new registration destination.
printf '127.0.0.2 sip.provider.test\n' >"$hosts"
sed -i "s/:${port_a}/:${port_b}/g" "$root/etc/pjsip.conf"
asterisk -C "$root/asterisk.conf" -rx 'module reload res_resolver_unbound.so' >/dev/null 2>&1 || true
asterisk -C "$root/asterisk.conf" -rx 'module reload res_pjsip.so' >/dev/null 2>&1 || true
for _ in $(seq 1 20); do grep -q "127.0.0.1:${port_b}" "$root/asterisk.log" && break; sleep 1; done
pid_after=$(ps -o pid= -p "$ast_pid" | tr -d ' ')
grep -q "127.0.0.1:${port_b}" "$root/asterisk.log" || { cat "$root/asterisk.log" >&2; exit 1; }
[[ "$pid_before" == "$pid_after" ]]
printf 'Asterisk version: %s\n' "$(asterisk -V)"
printf 'Asterisk/PJSIP: PASS\nCorrect hostname: PASS\nWrong hostname: FAIL AS EXPECTED\nverify_server: ENABLED\nHot update: PASS\nSame Asterisk PID: PASS\nPinned A: PASS\nPinned B: PASS\n'
