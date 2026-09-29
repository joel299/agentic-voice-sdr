#!/bin/sh
set -eu

CHAIN=FALEPACO_GRU142
IPTABLES=/usr/sbin/iptables
PROVIDER_IPS='177.11.49.223 177.11.49.224 177.11.49.225 177.11.49.226 177.11.49.230 177.11.49.231 177.11.49.8 177.11.49.22 177.11.49.36 177.11.49.111 177.11.49.107 177.11.49.196 177.11.49.199 177.11.49.13 177.11.49.31 177.11.49.82 177.11.49.71 177.11.49.143 177.11.49.174 177.11.49.234 177.11.49.97'

if ! "$IPTABLES" -S "$CHAIN" >/dev/null 2>&1; then
  "$IPTABLES" -N "$CHAIN"
fi
"$IPTABLES" -F "$CHAIN"
for ip in $PROVIDER_IPS; do
  "$IPTABLES" -A "$CHAIN" -s "$ip/32" -p tcp -m multiport --dports 5060,5061 -j ACCEPT
  "$IPTABLES" -A "$CHAIN" -s "$ip/32" -p udp -m multiport --dports 5060,5061,10000:65000 -j ACCEPT
done
"$IPTABLES" -A "$CHAIN" -p tcp -m multiport --dports 5060,5061 -j DROP
"$IPTABLES" -A "$CHAIN" -p udp -m multiport --dports 5060,5061,10000:65000 -j DROP
"$IPTABLES" -C INPUT -p tcp -m multiport --dports 5060,5061 -j "$CHAIN" 2>/dev/null || "$IPTABLES" -I INPUT 1 -p tcp -m multiport --dports 5060,5061 -j "$CHAIN"
"$IPTABLES" -C INPUT -p udp -m multiport --dports 5060,5061,10000:65000 -j "$CHAIN" 2>/dev/null || "$IPTABLES" -I INPUT 1 -p udp -m multiport --dports 5060,5061,10000:65000 -j "$CHAIN"

# Explicit egress policy is ACCEPT on this host; do not alter it.
