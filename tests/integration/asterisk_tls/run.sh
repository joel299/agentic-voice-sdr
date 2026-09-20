#!/usr/bin/env bash
set -euo pipefail

mode=${ASTERISK_GRU83_MODE:-unit}
if [[ "$mode" != real ]]; then
  printf 'Asterisk/PJSIP real gate: SKIP (set ASTERISK_GRU83_MODE=real)\n'
  exit 0
fi
for bin in openssl asterisk ps; do command -v "$bin" >/dev/null || { echo "$bin is required" >&2; exit 2; }; done
port=${ASTERISK_GRU83_TLS_PORT:-15061}
port_b=$((port + 1))
root=$(mktemp -d)
mkdir -p "$root"/{etc,var,run,log,keys,spool,agi}
cleanup() {
  local status=$?
  if [[ "$status" -ne 0 && -f "$root/asterisk.log" ]]; then cp "$root/asterisk.log" /tmp/gru83-last-asterisk.log; fi
  if [[ -n "${ast_pid:-}" ]]; then kill "$ast_pid" 2>/dev/null || true; wait "$ast_pid" 2>/dev/null || true; fi
  if [[ -n "${server_pid:-}" ]]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
  if [[ -n "${server_b_pid:-}" ]]; then kill "$server_b_pid" 2>/dev/null || true; wait "$server_b_pid" 2>/dev/null || true; fi
  if [[ -n "${dns_pid:-}" ]]; then kill "$dns_pid" 2>/dev/null || true; wait "$dns_pid" 2>/dev/null || true; fi
  rm -rf "$root"
}
trap cleanup EXIT

# Generate a disposable CA and hostname-only certificate.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=gru83-ca' \
  -keyout "$root/ca-key.pem" -out "$root/ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=sip.provider.test' \
  -keyout "$root/key.pem" -out "$root/req.pem" >/dev/null 2>&1
printf 'subjectAltName=DNS:sip.provider.test\n' >"$root/ext.cnf"
openssl x509 -req -in "$root/req.pem" -CA "$root/ca.pem" -CAkey "$root/ca-key.pem" \
  -CAcreateserial -days 1 -out "$root/cert.pem" -extfile "$root/ext.cnf" >/dev/null 2>&1

cat >"$root/etc/asterisk.conf" <<EOF
[directories]
astetcdir => $root/etc
astmoddir => /usr/lib/x86_64-linux-gnu/asterisk/modules
astvarlibdir => $root/var
astdbdir => $root/var
astkeydir => $root/keys
astdatadir => /usr/share/asterisk
astagidir => $root/agi
astspooldir => $root/spool
astrundir => $root/run
astlogdir => $root/log
astsbindir => /usr/sbin
[options]
verbose=3
EOF
cat >"$root/etc/modules.conf" <<'EOF'
[modules]
autoload=yes
noload => res_resolver_system.so
EOF
cat >"$root/etc/logger.conf" <<'EOF'
[general]
[logfiles]
console => notice,warning,error
EOF
cat >"$root/etc/manager.conf" <<'EOF'
[general]
enabled = no
EOF
cat >"$root/etc/extensions.conf" <<'EOF'
[default]
exten => s,1,Hangup()
EOF
hosts="$root/etc/.gru83-pinned.hosts"
printf '127.0.0.1 localhost\n127.0.0.1 sip.provider.test\n127.0.0.1 wrong.provider.test\n' >"$hosts"
dns_state="$root/dns-state"
printf '127.0.0.1\n' >"$dns_state"
python3 - "$dns_state" <<'PY' >"$root/dns.log" 2>&1 &
import socket, struct, sys, time
state=sys.argv[1]; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind(('127.0.0.1',53))
while True:
    q, addr=s.recvfrom(512); ident=q[:2]; i=12; labels=[]
    while q[i]: n=q[i]; i+=1; labels.append(q[i:i+n]); i+=n
    i+=1; question=q[12:i+4]; ip=open(state).read().strip(); answer=ident+b'\x81\x80'+struct.pack('!HHHH',1,1,0,0)+question+struct.pack('!HHHLH4s',0xc00c,1,1,1,4,socket.inet_aton(ip)); s.sendto(answer,addr)
PY
dns_pid=$!
resolver="$root/etc/resolver_unbound.conf"
cat >"$resolver" <<EOF
; managed by agentic-voice-sdr GRU-83; do not edit
[general]
hosts = $hosts
nameserver = 127.0.0.1
resolv = /dev/null
EOF
pjsip="$root/etc/pjsip.conf"
write_pjsip() {
  local host=$1
  cat >"$pjsip" <<EOF
[transport-tls]
type=transport
protocol=tls
method=tlsv1_2
bind=127.0.0.1:15060
cert_file=$root/cert.pem
priv_key_file=$root/key.pem
ca_list_file=$root/ca.pem
verify_server=yes
[auth]
type=auth
auth_type=userpass
username=test
password=test
[reg]
type=registration
transport=transport-tls
outbound_auth=auth
server_uri=sip:$host:$port
client_uri=sip:test@$host:$port
retry_interval=1
[aor]
type=aor
contact=sip:$host:$port
[endpoint]
type=endpoint
transport=transport-tls
aors=aor
outbound_auth=auth
context=default
EOF
}
write_pjsip sip.provider.test

openssl s_server -accept "$port" -cert "$root/cert.pem" -key "$root/key.pem" \
  -tls1_2 -quiet >"$root/server.log" 2>&1 & server_pid=$!
openssl s_server -accept "$port_b" -cert "$root/cert.pem" -key "$root/key.pem" \
  -tls1_2 -quiet >"$root/server-b.log" 2>&1 & server_b_pid=$!
asters=(unshare -m bash -c "mount --bind '$hosts' /etc/hosts && exec asterisk -C '$root/etc/asterisk.conf' -f -U root -G root -vv >'$root/asterisk.log' 2>&1")
"${asters[@]}" & ast_pid=$!
wait_log() { local pattern=$1; for _ in $(seq 1 20); do grep -Eq "$pattern" "$root/asterisk.log" && return 0; sleep 1; done; return 1; }
cli() {
  local output status
  set +e
  output=$(asterisk -C "$root/etc/asterisk.conf" -rx "$1" 2>&1)
  status=$?
  set -e
  printf '%s\n' "$output" >>"$root/cli.log"
  [[ "$status" -eq 0 ]] || { echo "Asterisk CLI failed: $1 (exit $status)" >&2; printf '%s\n' "$output" >&2; return 1; }
  printf '%s' "$output"
}

for _ in $(seq 1 20); do [[ -S "$root/run/asterisk.ctl" ]] && break; sleep 1; done
wait_log "Transport 'transport-tls' to remote 'sip.provider.test' - 127.0.0.1:$port - OK"
pid_before=$(ps -o pid= -p "$ast_pid" | tr -d ' ')
cli 'pjsip show transport transport-tls' | grep -qi 'verify_server[[:space:]]*: Yes'

# Negative identity test: same certificate, wrong logical hostname, Asterisk remains the TLS client.
write_pjsip wrong.provider.test
printf '127.0.0.1 localhost\n127.0.0.1 sip.provider.test\n127.0.0.1 wrong.provider.test\n' >"$hosts"
cli 'module reload res_resolver_unbound.so' | grep -qiE 'reloaded successfully|Reloading module'
cli 'module reload res_pjsip.so' | grep -qiE 'reloaded successfully|Reloading module'
cli 'pjsip send register reg' >/dev/null
wait_log 'does not match to any identities specified in the certificate|hostname mismatch|certificate verify failed'

# Restore logical hostname, then change only the pinned address to 127.0.0.2.
write_pjsip sip.provider.test
printf '127.0.0.2 sip.provider.test\n' >"$hosts"
printf '127.0.0.2\n' >"$dns_state"
sed -i "s/:$port/:$port_b/g; s/127.0.0.1:$port_b/127.0.0.2:$port_b/g" "$pjsip"
cli 'module reload res_resolver_unbound.so' | grep -qiE 'reloaded successfully|Reloading module'
cli 'core reload' >/dev/null
cli 'module reload res_pjsip.so' | grep -qiE 'reloaded successfully|Reloading module'
cli 'pjsip send unregister reg' >/dev/null
cli 'pjsip send register reg' >/dev/null
wait_log "Transport 'transport-tls' to remote 'sip.provider.test' - 127.0.0.2:$port_b - OK"
pid_after=$(ps -o pid= -p "$ast_pid" | tr -d ' ')
[[ "$pid_before" == "$pid_after" ]]

printf 'Asterisk version: %s\n' "$(asterisk -V)"
printf 'Asterisk/PJSIP: PASS\nCorrect hostname: PASS\nWrong hostname: FAIL AS EXPECTED\nverify_server: ENABLED\nResolver reload: PASS\nPJSIP reload: PASS\nPinned A: PASS\nPinned B: PASS\nHot update: PASS\nSame Asterisk PID: PASS\n'
