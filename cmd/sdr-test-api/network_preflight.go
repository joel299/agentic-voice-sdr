package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var falePacoProviderIPs = []string{
	"177.11.49.223", "177.11.49.224", "177.11.49.225", "177.11.49.226", "177.11.49.230", "177.11.49.231", "177.11.49.8", "177.11.49.22", "177.11.49.36", "177.11.49.111", "177.11.49.107", "177.11.49.196", "177.11.49.199", "177.11.49.13", "177.11.49.31", "177.11.49.82", "177.11.49.71", "177.11.49.143", "177.11.49.174", "177.11.49.234", "177.11.49.97",
}

type networkPreflightResponse struct {
	ProviderIPAllowlistCount int      `json:"provider_ip_allowlist_count"`
	ProviderIPsConfigured    bool     `json:"provider_ips_configured"`
	TCP5060EgressReady       bool     `json:"tcp_5060_egress_ready"`
	TCP5061EgressReady       bool     `json:"tcp_5061_egress_ready"`
	UDP5060RouteReady        bool     `json:"udp_5060_route_ready"`
	UDP5061RouteReady        bool     `json:"udp_5061_route_ready"`
	RTPUDPStart              int      `json:"rtp_udp_start"`
	RTPUDPEnd                int      `json:"rtp_udp_end"`
	RTPUDPRangeConfigured    bool     `json:"rtp_udp_range_configured"`
	AsteriskTCP5060Listening bool     `json:"asterisk_tcp_5060_listening"`
	AsteriskUDP5060Listening bool     `json:"asterisk_udp_5060_listening"`
	AsteriskTCP5061Listening bool     `json:"asterisk_tcp_5061_listening"`
	AsteriskUDP5061Listening bool     `json:"asterisk_udp_5061_listening"`
	LocalFirewallDetected    bool     `json:"local_firewall_detected"`
	FirewallProviderRules    bool     `json:"firewall_provider_rules_present"`
	SIPSignalingRulesReady   bool     `json:"sip_signaling_rules_ready"`
	RTPRulesReady            bool     `json:"rtp_rules_ready"`
	UDPRemotePortConfirmed   bool     `json:"udp_remote_port_confirmed"`
	Blockers                 []string `json:"blockers"`
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
	response := networkPreflightResponse{ProviderIPAllowlistCount: len(falePacoProviderIPs), ProviderIPsConfigured: len(falePacoProviderIPs) == 21, Blockers: []string{}, UDPRemotePortConfirmed: false}
	response.TCP5060EgressReady = providerTCPReachable(5060)
	response.TCP5061EgressReady = providerTCPReachable(5061)
	response.UDP5060RouteReady, response.UDP5061RouteReady = localEgressPolicyReady()
	response.LocalFirewallDetected = localFirewallDetected()
	// Provider-level firewall rules are not observable from this host.
	response.FirewallProviderRules = false
	response.SIPSignalingRulesReady = response.LocalFirewallDetected && response.UDP5060RouteReady && response.UDP5061RouteReady && providerFirewallAllowlistReady()
	response.RTPUDPStart, response.RTPUDPEnd, response.RTPUDPRangeConfigured = readRTPRange("/etc/asterisk/rtp.conf")
	response.AsteriskTCP5060Listening, response.AsteriskUDP5060Listening = asteriskListening(5060)
	response.AsteriskTCP5061Listening, response.AsteriskUDP5061Listening = asteriskListening(5061)
	response.RTPRulesReady = response.RTPUDPRangeConfigured && response.UDP5060RouteReady && providerFirewallAllowlistReady()
	response.FirewallProviderRules = false // Cloud/VPS security-group state is not exposed by this host.
	if !response.TCP5060EgressReady {
		response.Blockers = append(response.Blockers, "tcp_5060_provider_reachability_unconfirmed")
	}
	if !response.TCP5061EgressReady {
		response.Blockers = append(response.Blockers, "tcp_5061_provider_reachability_unconfirmed")
	}
	if !response.RTPUDPRangeConfigured {
		response.Blockers = append(response.Blockers, "asterisk_rtp_range_not_configured")
	}
	if !response.SIPSignalingRulesReady {
		response.Blockers = append(response.Blockers, "host_sip_allowlist_rules_missing")
	}
	if !response.RTPRulesReady {
		response.Blockers = append(response.Blockers, "host_rtp_allowlist_rules_missing")
	}
	if !response.FirewallProviderRules {
		response.Blockers = append(response.Blockers, "cloud_firewall_rules_unverified")
	}
	jsonOut(w, http.StatusOK, response)
}

func providerTCPReachable(port int) bool {
	for _, ip := range falePacoProviderIPs {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		cancel()
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

func localEgressPolicyReady() (bool, bool) {
	out, err := exec.Command("iptables", "-S", "OUTPUT").Output()
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "-P OUTPUT ACCEPT") {
			return true, true
		}
	}
	return false, false
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
