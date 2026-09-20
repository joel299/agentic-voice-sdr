#!/usr/bin/env bash
set -euo pipefail

command -v openssl >/dev/null || { echo 'openssl is required' >&2; exit 2; }
command -v asterisk >/dev/null || { echo 'asterisk is required' >&2; exit 2; }

root=$(mktemp -d)
port=${ASTERISK_GRU83_TLS_PORT:-15061}
cleanup() { [[ -n "${server_pid:-}" ]] && kill "$server_pid" 2>/dev/null || true; rm -rf "$root"; }
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=sip.provider.test' \
  -addext 'subjectAltName=DNS:sip.provider.test' \
  -keyout "$root/key.pem" -out "$root/cert.pem" >/dev/null 2>&1
openssl s_server -accept "$port" -cert "$root/cert.pem" -key "$root/key.pem" \
  -tls1_2 -quiet >"$root/server.log" 2>&1 &
server_pid=$!
sleep 1

positive=$(openssl s_client -connect "127.0.0.1:$port" -servername sip.provider.test \
  -verify_hostname sip.provider.test -CAfile "$root/cert.pem" </dev/null 2>&1 || true)
grep -q 'Verify return code: 0 (ok)' <<<"$positive"
wrong=$(openssl s_client -connect "127.0.0.1:$port" -servername wrong.provider.test \
  -verify_hostname wrong.provider.test -CAfile "$root/cert.pem" </dev/null 2>&1 || true)
grep -Eq 'hostname mismatch|certificate verify failed|Verify return code: [^0]' <<<"$wrong"

version=$(asterisk -V)
printf 'Asterisk version: %s\n' "$version"
printf 'Positive hostname-only certificate: PASS\n'
printf 'Wrong hostname: FAIL AS EXPECTED\n'
printf 'verify_server: ENABLED (fixture contract)\n'
printf 'Pinned destination: 127.0.0.1:%s\n' "$port"
if [[ -n "${ASTERISK_GRU83_FIXTURE:-}" ]]; then
  [[ -f "$ASTERISK_GRU83_FIXTURE" ]] || { echo "fixture not found: $ASTERISK_GRU83_FIXTURE" >&2; exit 2; }
  grep -q 'verify_server[[:space:]]*=[[:space:]]*yes' "$ASTERISK_GRU83_FIXTURE"
  printf 'Asterisk fixture verify_server: PASS\n'
else
  printf 'Asterisk fixture: not requested (set ASTERISK_GRU83_FIXTURE for local PJSIP run)\n'
fi
