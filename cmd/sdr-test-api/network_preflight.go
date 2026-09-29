package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

var falePacoProviderIPs = []string{
	"177.11.49.223", "177.11.49.224", "177.11.49.225", "177.11.49.226", "177.11.49.230", "177.11.49.231", "177.11.49.8", "177.11.49.22", "177.11.49.36", "177.11.49.111", "177.11.49.107", "177.11.49.196", "177.11.49.199", "177.11.49.13", "177.11.49.31", "177.11.49.82", "177.11.49.71", "177.11.49.143", "177.11.49.174", "177.11.49.234", "177.11.49.97",
}

type networkPreflightResponse struct {
	ProviderAddressDNSState            string   `json:"provider_address_dns_state"`
	ProviderAddressResolvedIPsCount    int      `json:"provider_address_resolved_ips_count"`
	ProviderAddressResolvesToAllowlist bool     `json:"provider_address_resolves_to_allowlist"`
	ProviderAddressResolvedIPs         []string `json:"provider_address_resolved_ips"`
	RequestURIHostDNSState             string   `json:"request_uri_host_dns_state"`
	RequestURIHostResolvedIPsCount     int      `json:"request_uri_host_resolved_ips_count"`
	RequestURIHostResolvesToAllowlist  bool     `json:"request_uri_host_resolves_to_allowlist"`
	RequestURIHostResolvedIPs          []string `json:"request_uri_host_resolved_ips"`
	OutboundProxyDNSState              string   `json:"outbound_proxy_dns_state"`
	OutboundProxyResolvesToAllowlist   bool     `json:"outbound_proxy_resolves_to_allowlist"`
	DNSTimeoutMS                       int      `json:"dns_timeout_ms"`
	ProviderIPAllowlistCount           int      `json:"provider_ip_allowlist_count"`
	ProviderIPsConfigured              bool     `json:"provider_ips_configured"`
	TCP5060TestedCount                 int      `json:"tcp_5060_tested_count"`
	TCP5060ReachableCount              int      `json:"tcp_5060_reachable_count"`
	TCP5060AnyReachable                bool     `json:"tcp_5060_any_reachable"`
	TCP5060ProbeComplete               bool     `json:"tcp_5060_probe_complete"`
	TCP5061TestedCount                 int      `json:"tcp_5061_tested_count"`
	TCP5061ReachableCount              int      `json:"tcp_5061_reachable_count"`
	TCP5061AnyReachable                bool     `json:"tcp_5061_any_reachable"`
	TCP5061ProbeComplete               bool     `json:"tcp_5061_probe_complete"`
	UDP5060RouteReady                  bool     `json:"udp_5060_route_ready"`
	UDP5061RouteReady                  bool     `json:"udp_5061_route_ready"`
	RTPUDPStart                        int      `json:"rtp_udp_start"`
	RTPUDPEnd                          int      `json:"rtp_udp_end"`
	RTPUDPRangeConfigured              bool     `json:"rtp_udp_range_configured"`
	AsteriskTCP5060Listening           bool     `json:"asterisk_tcp_5060_listening"`
	AsteriskUDP5060Listening           bool     `json:"asterisk_udp_5060_listening"`
	AsteriskTCP5061Listening           bool     `json:"asterisk_tcp_5061_listening"`
	AsteriskUDP5061Listening           bool     `json:"asterisk_udp_5061_listening"`
	LocalFirewallDetected              bool     `json:"local_firewall_detected"`
	HostFirewallRulesReady             bool     `json:"host_firewall_rules_ready"`
	FirewallProviderRules              bool     `json:"firewall_provider_rules_present"`
	CloudFirewallState                 string   `json:"cloud_firewall_state"`
	IPTablesOutputPolicy               string   `json:"iptables_output_policy"`
	NFTablesDetected                   bool     `json:"nftables_detected"`
	HostEgressPolicyState              string   `json:"host_egress_policy_state"`
	EffectiveEgressState               string   `json:"effective_egress_state"`
	SIPSignalingRulesReady             bool     `json:"sip_signaling_rules_ready"`
	RTPRulesReady                      bool     `json:"rtp_rules_ready"`
	UDPRemotePortConfirmed             bool     `json:"udp_remote_port_confirmed"`
	Warnings                           []string `json:"warnings"`
	Blockers                           []string `json:"blockers"`
}

func (s *server) networkPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := bearer(r); !ok {
		jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	response := networkPreflightResponse{ProviderIPAllowlistCount: len(falePacoProviderIPs), ProviderIPsConfigured: len(falePacoProviderIPs) == 21, Blockers: []string{}, Warnings: []string{}, UDPRemotePortConfirmed: false}
	response.DNSTimeoutMS = 4000
	if m, err := dotenv(sipEnv); err == nil {
		proxyHost, _, _ := net.SplitHostPort(m["FALEPACO_SIP_OUTBOUND_PROXY"])
		response.ProviderAddressDNSState, response.ProviderAddressResolvedIPs, response.ProviderAddressResolvesToAllowlist = resolveFalePacoHost(m["FALEPACO_SIP_DOMAIN"], 4*time.Second)
		response.ProviderAddressResolvedIPsCount = len(response.ProviderAddressResolvedIPs)
		response.RequestURIHostDNSState, response.RequestURIHostResolvedIPs, response.RequestURIHostResolvesToAllowlist = resolveFalePacoHost(m["FALEPACO_SIP_OUTBOUND_HOST"], 4*time.Second)
		response.RequestURIHostResolvedIPsCount = len(response.RequestURIHostResolvedIPs)
		response.OutboundProxyDNSState, _, response.OutboundProxyResolvesToAllowlist = resolveFalePacoHost(proxyHost, 4*time.Second)
	} else {
		response.ProviderAddressDNSState, response.RequestURIHostDNSState, response.OutboundProxyDNSState = "configuration_unavailable", "configuration_unavailable", "configuration_unavailable"
	}
	for _, state := range []string{response.ProviderAddressDNSState, response.RequestURIHostDNSState, response.OutboundProxyDNSState} {
		if state != "resolved" {
			response.Blockers = append(response.Blockers, "falepaco_dns_"+state)
			break
		}
	}
	response.TCP5060TestedCount, response.TCP5060ReachableCount = providerTCPReachable(5060)
	response.TCP5060AnyReachable = response.TCP5060ReachableCount > 0
	response.TCP5060ProbeComplete = response.TCP5060TestedCount == len(falePacoProviderIPs)
	response.TCP5061TestedCount, response.TCP5061ReachableCount = providerTCPReachable(5061)
	response.TCP5061AnyReachable = response.TCP5061ReachableCount > 0
	response.TCP5061ProbeComplete = response.TCP5061TestedCount == len(falePacoProviderIPs)
	response.IPTablesOutputPolicy, response.NFTablesDetected, response.HostEgressPolicyState = inspectHostEgressPolicy()
	response.CloudFirewallState = "unknown"
	response.Warnings = append(response.Warnings, "cloud_firewall_unverified")
	response.FirewallProviderRules = false
	response.EffectiveEgressState = response.HostEgressPolicyState
	if response.CloudFirewallState == "blocked" || response.HostEgressPolicyState == "blocked" {
		response.EffectiveEgressState = "blocked"
	} else if response.CloudFirewallState == "unknown" || response.HostEgressPolicyState == "unknown" {
		response.EffectiveEgressState = "unknown"
	}
	response.UDP5060RouteReady = response.HostEgressPolicyState == "ready"
	response.UDP5061RouteReady = response.HostEgressPolicyState == "ready"
	response.LocalFirewallDetected = localFirewallDetected()
	response.HostFirewallRulesReady = providerFirewallAllowlistReady()
	response.SIPSignalingRulesReady = response.LocalFirewallDetected && response.HostFirewallRulesReady
	response.RTPUDPStart, response.RTPUDPEnd, response.RTPUDPRangeConfigured = readRTPRange("/etc/asterisk/rtp.conf")
	response.AsteriskTCP5060Listening, response.AsteriskUDP5060Listening = asteriskListening(5060)
	response.AsteriskTCP5061Listening, response.AsteriskUDP5061Listening = asteriskListening(5061)
	response.RTPRulesReady = response.RTPUDPRangeConfigured && response.HostFirewallRulesReady
	if !response.TCP5060ProbeComplete {
		response.Warnings = append(response.Warnings, "tcp_5060_probe_partial")
	}
	if !response.TCP5061ProbeComplete {
		response.Warnings = append(response.Warnings, "tcp_5061_probe_partial")
	}
	if response.HostEgressPolicyState == "blocked" {
		response.Blockers = append(response.Blockers, "host_egress_policy_blocks_provider")
	}
	if !response.HostFirewallRulesReady {
		response.Blockers = append(response.Blockers, "host_provider_allowlist_missing")
	}
	if !response.RTPUDPRangeConfigured {
		response.Blockers = append(response.Blockers, "asterisk_rtp_range_not_configured")
	}
	jsonOut(w, http.StatusOK, response)
}

func resolveFalePacoHost(host string, timeout time.Duration) (string, []string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "timeout", []string{}, false
		}
		if strings.Contains(strings.ToLower(err.Error()), "no such host") {
			return "no_answer", []string{}, false
		}
		return "resolver_error", []string{}, false
	}
	allowed := make(map[string]bool, len(falePacoProviderIPs))
	for _, ip := range falePacoProviderIPs {
		allowed[ip] = true
	}
	set, safe := map[string]bool{}, len(ips) > 0
	for _, entry := range ips {
		value := entry.IP.String()
		set[value] = true
		if !allowed[value] {
			safe = false
		}
	}
	result := make([]string, 0, len(set))
	for ip := range set {
		result = append(result, ip)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return "no_answer", result, false
	}
	if !safe {
		return "outside_allowlist", result, false
	}
	return "resolved", result, true
}

func verifyFalePacoDNS(host string, timeout time.Duration) (string, string) {
	state, _, allowed := resolveFalePacoHost(host, timeout)
	if allowed {
		return "", ""
	}
	switch state {
	case "timeout":
		return "provider_dns_timeout", "DNS resolution timed out"
	case "no_answer":
		return "provider_dns_no_answer", "DNS returned no addresses"
	case "outside_allowlist":
		return "provider_dns_outside_allowlist", "DNS answer is outside the Fale Paco allowlist"
	default:
		return "provider_dns_error", "DNS resolver failed"
	}
}

func providerTCPReachable(port int) (tested, reachable int) {
	for _, ip := range falePacoProviderIPs {
		tested++
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		cancel()
		if err == nil {
			reachable++
			_ = conn.Close()
		}
	}
	return tested, reachable
}

func probeTCPAddresses(port int, dial func(context.Context, string, string) (net.Conn, error)) (tested, reachable int) {
	for _, ip := range falePacoProviderIPs {
		tested++
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		cancel()
		if err == nil {
			reachable++
			_ = conn.Close()
		}
	}
	return tested, reachable
}

func inspectHostEgressPolicy() (string, bool, string) {
	policy := "unknown"
	if out, err := exec.Command("iptables", "-S").Output(); err == nil {
		policy = iptablesEgressState(string(out))
	}
	nftDetected := false
	if _, err := exec.LookPath("nft"); err == nil {
		nftDetected = true
	}
	state := "unknown"
	if nftDetected {
		out, err := exec.Command("nft", "list", "ruleset").Output()
		if err != nil {
			state = "unknown"
		} else if nftBlocksFalePaco(string(out)) {
			state = "blocked"
		} else if policy == "ready" {
			state = "ready"
		}
	} else {
		state = policy
	}
	if policy == "blocked" {
		state = "blocked"
	}
	return policy, nftDetected, state
}

func nftBlocksFalePaco(rules string) bool {
	var inOutputChain, sawOutputBase, outputPolicyDrop bool
	for _, line := range strings.Split(rules, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "chain ") {
			inOutputChain = false
			if strings.Contains(trimmed, "hook output") {
				inOutputChain = true
				sawOutputBase = true
				if strings.Contains(trimmed, "policy drop") || strings.Contains(trimmed, "policy reject") {
					outputPolicyDrop = true
				}
			}
			continue
		}
		if !inOutputChain {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "drop") || strings.Contains(lower, "reject") {
			return true
		}
	}
	return sawOutputBase && outputPolicyDrop
}

func summarizeFirewallRule(line string) (action, proto, destination string) {
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "-p":
			if i+1 < len(fields) {
				proto = fields[i+1]
			}
		case "-d":
			if i+1 < len(fields) {
				destination = fields[i+1]
			}
		case "-j":
			if i+1 < len(fields) {
				action = fields[i+1]
			}
		}
	}
	return
}

func iptablesEgressState(rules string) string {
	policy := "unknown"
	chains := make(map[string]string)
	outputRules := []string{}
	for _, line := range strings.Split(rules, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "-P" {
			chains[fields[1]] = fields[2]
			if fields[1] == "OUTPUT" {
				policy = fields[2]
			}
		}
		if len(fields) >= 3 && fields[0] == "-A" && fields[1] == "OUTPUT" {
			outputRules = append(outputRules, line)
		}
	}
	if policy == "DROP" || policy == "REJECT" {
		return "blocked"
	}
	if policy != "ACCEPT" {
		return "unknown"
	}
	for _, line := range outputRules {
		fields := strings.Fields(line)
		jump := ""
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "-j" {
				jump = fields[i+1]
				break
			}
		}
		if jump != "" && (chains[jump] == "DROP" || chains[jump] == "REJECT") {
			return "blocked"
		}
		if jump == "DROP" || jump == "REJECT" {
			return "blocked"
		}
		if jump != "" && jump != "ACCEPT" && chains[jump] == "" {
			return "unknown"
		}
		if jump == "ACCEPT" || (jump != "" && chains[jump] == "ACCEPT") {
			continue
		}
		action, proto, dst := summarizeFirewallRule(line)
		if action != "DROP" && action != "REJECT" {
			continue
		}
		if dst == "177.11.49.0/24" || dst == "177.11.49.0/255.255.255.0" {
			return "blocked"
		}
		for _, ip := range falePacoProviderIPs {
			if dst == ip+"/32" {
				return "blocked"
			}
		}
		if proto == "tcp" || proto == "udp" {
			if strings.Contains(line, "5060") || strings.Contains(line, "5061") || strings.Contains(line, "10000:65000") {
				return "blocked"
			}
		}
	}
	return "ready"
}

func localFirewallDetected() bool {
	for _, name := range []string{"iptables", "nft"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func providerFirewallAllowlistReady() bool {
	out, err := exec.Command("iptables", "-S", "FALEPACO_GRU142").Output()
	if err != nil {
		return false
	}
	rules := string(out)
	for _, ip := range falePacoProviderIPs {
		tcp := "-A FALEPACO_GRU142 -s " + ip + "/32 -p tcp -m multiport --dports 5060,5061 -j ACCEPT"
		udp := "-A FALEPACO_GRU142 -s " + ip + "/32 -p udp -m multiport --dports 5060,5061,10000:65000 -j ACCEPT"
		if !strings.Contains(rules, tcp) || !strings.Contains(rules, udp) {
			return false
		}
	}
	return strings.Contains(rules, "-A FALEPACO_GRU142 -p tcp -m multiport --dports 5060,5061 -j DROP") && strings.Contains(rules, "-A FALEPACO_GRU142 -p udp -m multiport --dports 5060,5061,10000:65000 -j DROP")
}

func readRTPRange(path string) (int, int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	inGeneral, foundStart, foundEnd := false, false, false
	start, end := 0, 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inGeneral = strings.EqualFold(line, "[general]")
			continue
		}
		if !inGeneral || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "rtpstart":
			start, foundStart = parsed, true
		case "rtpend":
			end, foundEnd = parsed, true
		}
	}
	return start, end, foundStart && foundEnd && start == 10000 && end == 65000
}

func asteriskListening(port int) (tcp, udp bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ss", "-lntup").Output()
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, ":"+strconv.Itoa(port)) || !strings.Contains(line, "asterisk") {
			continue
		}
		protocol := strings.TrimSpace(line)
		if strings.HasPrefix(protocol, "tcp") {
			tcp = true
		}
		if strings.HasPrefix(protocol, "udp") {
			udp = true
		}
	}
	return tcp, udp
}
