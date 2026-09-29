package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFalePacoTCPResolverForcesTCPAndUsesSystemAddress(t *testing.T) {
	var gotNetwork, gotAddress string
	resolver := newFalePacoTCPResolver(func(_ context.Context, network, address string) (net.Conn, error) {
		gotNetwork, gotAddress = network, address
		return fakeConn{}, nil
	})
	conn, err := resolver.Dial(context.Background(), "udp", "23.19.52.52:53")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if gotNetwork != "tcp" || gotAddress != "23.19.52.52:53" {
		t.Fatalf("network=%s address=%s", gotNetwork, gotAddress)
	}
}

func TestFalePacoTCPResolverLiveSystemDNS(t *testing.T) {
	if os.Getenv("RUN_FALEPACO_DNS_TCP_INTEGRATION") != "1" {
		t.Skip("set RUN_FALEPACO_DNS_TCP_INTEGRATION=1 on the VPS to test configured DNS over TCP")
	}
	for host, want := range map[string]string{"98034.falepaco.com.br": "177.11.49.36", "96678.falepaco.com.br": "177.11.49.97"} {
		state, ips, allowed := resolveFalePacoHost(host, 4*time.Second)
		found := false
		for _, ip := range ips {
			if ip == want {
				found = true
			}
		}
		if state != "resolved" || !allowed || !found {
			t.Fatalf("host=%s state=%s ips=%v allowed=%v", host, state, ips, allowed)
		}
	}
}

func TestResolveFalePacoHostClassifiesTimeoutNoAnswerAndOutsideAllowlist(t *testing.T) {
	original := lookupFalePacoIPs
	defer func() { lookupFalePacoIPs = original }()
	lookupFalePacoIPs = func(context.Context, string) ([]net.IPAddr, error) { return nil, context.DeadlineExceeded }
	state, _, ok := resolveFalePacoHost("host", time.Second)
	if state != "timeout" || ok {
		t.Fatalf("timeout classified as %s", state)
	}
	lookupFalePacoIPs = func(context.Context, string) ([]net.IPAddr, error) { return nil, &net.DNSError{Err: "no such host"} }
	state, _, ok = resolveFalePacoHost("host", time.Second)
	if state != "no_answer" || ok {
		t.Fatalf("no-answer classified as %s", state)
	}
	lookupFalePacoIPs = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.168.1.1")}}, nil
	}
	state, _, ok = resolveFalePacoHost("host", time.Second)
	if state != "outside_allowlist" || ok {
		t.Fatalf("unsafe IP classified as %s", state)
	}
}

func TestSafeApplyErrorReportsStageAndRedactsSecrets(t *testing.T) {
	stage, class, summary := safeApplyError(errors.New("asterisk reload failed: password=topsecret"), "topsecret")
	if stage != "reload" || class != "pjsip_configuration_error" || strings.Contains(summary, "topsecret") || !strings.Contains(summary, "[REDACTED]") {
		t.Fatalf("unsafe or incomplete apply diagnostics: stage=%q class=%q summary=%q", stage, class, summary)
	}
}
func TestResolveFalePacoHostChecksAllAnswersAgainstOfficialAllowlist(t *testing.T) {
	original := lookupFalePacoIPs
	defer func() { lookupFalePacoIPs = original }()
	lookupFalePacoIPs = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("177.11.49.36")}, {IP: net.ParseIP("177.11.49.97")}}, nil
	}
	state, ips, allowed := resolveFalePacoHost("example-controlled-host", time.Second)
	if state != "resolved" || !allowed || len(ips) != 2 {
		t.Fatalf("state=%s ips=%v allowed=%v", state, ips, allowed)
	}
	lookupFalePacoIPs = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("177.11.49.36")}, {IP: net.ParseIP("192.168.1.10")}}, nil
	}
	state, ips, allowed = resolveFalePacoHost("example-controlled-host", time.Second)
	if state != "outside_allowlist" || allowed || len(ips) != 2 {
		t.Fatalf("mixed answer must fail closed: state=%s ips=%v allowed=%v", state, ips, allowed)
	}
}

func TestFalePacoSaveValidationIsDNSIndependentAndStrictlyAllowlisted(t *testing.T) {
	cfg := falepacoConfigRequest{ProviderAddress: "98034.falepaco.com.br", RequestURIHost: "96678.falepaco.com.br", OutboundProxy: "98034.falepaco.com.br:5060", Username: "100", Extension: "100", CallerID: "551155200455", Transport: "tcp", Port: 5060, Registration: RegistrationConfig{Enabled: true, ServerURI: "sip:96678.falepaco.com.br:5060", ClientURI: "sip:100@96678.falepaco.com.br:5060", ContactUser: "100", Realm: "96678.falepaco.com.br", RetryIntervalSeconds: 60, MaxRetries: 3}}
	if got := validateFalePacoSave(cfg); got != nil {
		t.Fatalf("approved config should validate without live DNS: %+v", got)
	}
	cfg.ProviderAddress = "example.com"
	if got := validateFalePacoSave(cfg); got == nil || got.Field != "provider_address" || got.Class != "host_not_allowed" {
		t.Fatalf("external host should be rejected precisely: %+v", got)
	}
	cfg.ProviderAddress = "127.0.0.1"
	if got := validateFalePacoSave(cfg); got == nil || got.Class != "unsafe_literal_ip" {
		t.Fatalf("IP literal should be rejected: %+v", got)
	}
}

func TestResolveFalePacoHostChecksEntireAllowlist(t *testing.T) {
	allow := map[string]bool{}
	for _, ip := range falePacoProviderIPs {
		allow[ip] = true
	}
	if len(allow) != 21 || !allow["177.11.49.36"] || !allow["177.11.49.97"] {
		t.Fatalf("official IP set incorrect: %d", len(allow))
	}
}

func TestProviderAllowlistContainsOfficialCount(t *testing.T) {
	if len(falePacoProviderIPs) != 21 {
		t.Fatalf("provider allowlist count=%d", len(falePacoProviderIPs))
	}
}

func TestSavedFalepacoRoundTripsRegistrationFieldsAndRejectsConflicts(t *testing.T) {
	values := map[string]string{
		"FALEPACO_SIP_DOMAIN": "provider.example", "FALEPACO_SIP_OUTBOUND_HOST": "request.example",
		"FALEPACO_SIP_OUTBOUND_PROXY": "proxy.example:5060", "FALEPACO_SIP_USERNAME": "100",
		"FALEPACO_SIP_EXTENSION": "100", "FALEPACO_SIP_PASSWORD": "secret-never-return",
		"FALEPACO_SIP_CALLER_ID": "551155200455", "FALEPACO_SIP_TRANSPORT": "tcp", "FALEPACO_SIP_PORT": "5060",
		"FALEPACO_SIP_REGISTRATION_ENABLED": "true", "FALEPACO_SIP_REGISTRATION_REQUIRED": "true",
		"FALEPACO_SIP_REGISTRATION_SERVER_URI": "sip:request.example:5060",
		"FALEPACO_SIP_REGISTRATION_CLIENT_URI": "sip:100@request.example:5060",
		"FALEPACO_SIP_CONTACT_USER":            "100", "FALEPACO_SIP_REALM": "request.example",
		"FALEPACO_SIP_RETRY_INTERVAL": "45", "FALEPACO_SIP_MAX_RETRIES": "7",
	}
	testEnv := filepath.Join(t.TempDir(), "falepaco.env")
	input := falepacoConfigRequest{ProviderAddress: values["FALEPACO_SIP_DOMAIN"], RequestURIHost: values["FALEPACO_SIP_OUTBOUND_HOST"], OutboundProxy: values["FALEPACO_SIP_OUTBOUND_PROXY"], Username: values["FALEPACO_SIP_USERNAME"], Extension: values["FALEPACO_SIP_EXTENSION"], CallerID: values["FALEPACO_SIP_CALLER_ID"], Transport: values["FALEPACO_SIP_TRANSPORT"], Port: 5060, Registration: RegistrationConfig{Enabled: true, ServerURI: values["FALEPACO_SIP_REGISTRATION_SERVER_URI"], ClientURI: values["FALEPACO_SIP_REGISTRATION_CLIENT_URI"], ContactUser: values["FALEPACO_SIP_CONTACT_USER"], Realm: values["FALEPACO_SIP_REALM"], RetryIntervalSeconds: 45, MaxRetries: 7}}
	if err := atomicDotenvWriteAt(testEnv, falepacoEnv(input, values["FALEPACO_SIP_PASSWORD"])); err != nil {
		t.Fatal(err)
	}
	values, err := dotenv(testEnv)
	if err != nil {
		t.Fatal(err)
	}
	req, err := savedFalepaco(values)
	if err != nil {
		t.Fatal(err)
	}
	if !req.RegistrationRequired || req.RegistrationServerURI != "sip:request.example:5060" || req.RegistrationClientURI != "sip:100@request.example:5060" || req.RegistrationContactUser != "100" || req.RegistrationRealm != "request.example" || req.RegistrationRetryInterval != 45 || req.RegistrationMaxRetries != 7 {
		t.Fatalf("registration config did not round-trip: %+v", req)
	}
	response := falepacoConfigResponse{ProviderAddress: values["FALEPACO_SIP_DOMAIN"], RequestURIHost: values["FALEPACO_SIP_OUTBOUND_HOST"], OutboundProxy: req.OutboundProxy, Username: req.Auth.Username, Extension: req.FromUser, PasswordConfigured: true, CallerID: req.CallerID, Transport: req.Transport, Port: req.Port, Registration: RegistrationConfig{Enabled: req.RegistrationRequired, ServerURI: req.RegistrationServerURI, ClientURI: req.RegistrationClientURI, ContactUser: req.RegistrationContactUser, Realm: req.RegistrationRealm, RetryIntervalSeconds: req.RegistrationRetryInterval, MaxRetries: req.RegistrationMaxRetries}}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-never-return") || strings.Contains(string(encoded), `"password"`) {
		t.Fatalf("secret leaked in GET response: %s", encoded)
	}
	values["FALEPACO_SIP_REGISTRATION_REQUIRED"] = "false"
	if _, err := savedFalepaco(values); err == nil || !strings.Contains(err.Error(), "invalid_registration_configuration") {
		t.Fatalf("conflicting flags should fail, err=%v", err)
	}
}

func TestSavedFalepacoRegistrationDefaultsAndInvalidRetry(t *testing.T) {
	values := map[string]string{"FALEPACO_SIP_REGISTRATION_ENABLED": "true", "FALEPACO_SIP_REGISTRATION_REQUIRED": "true"}
	req, err := savedFalepaco(values)
	if err != nil || req.RegistrationRetryInterval != 60 || req.RegistrationMaxRetries != 3 {
		t.Fatalf("defaults req=%+v err=%v", req, err)
	}
	values["FALEPACO_SIP_RETRY_INTERVAL"] = "60junk"
	if _, err := savedFalepaco(values); err == nil {
		t.Fatal("invalid retry interval was silently ignored")
	}
}

type fakeConn struct{}

func (fakeConn) Read([]byte) (int, error)         { return 0, nil }
func (fakeConn) Write([]byte) (int, error)        { return 0, nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return nil }
func (fakeConn) RemoteAddr() net.Addr             { return nil }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

func TestProbeTCPAddressesTestsAllIPsAndCountsOnlyReachable(t *testing.T) {
	tested, reachable := probeTCPAddresses(5060, func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasSuffix(address, ":5060") && strings.Contains(address, "177.11.49.223") {
			return fakeConn{}, nil
		}
		return nil, errors.New("unreachable")
	})
	if tested != len(falePacoProviderIPs) || reachable != 1 {
		t.Fatalf("tested=%d reachable=%d", tested, reachable)
	}
}

func TestIPTablesEgressStateDetectsPolicyAndRuleBlocks(t *testing.T) {
	if got := iptablesEgressState("-P OUTPUT ACCEPT\n-A OUTPUT -d 177.11.49.223/32 -p tcp --dport 5060 -j DROP\n"); got != "blocked" {
		t.Fatalf("state=%q", got)
	}
	if got := iptablesEgressState("-P OUTPUT DROP\n"); got != "blocked" {
		t.Fatalf("state=%q", got)
	}
	if got := iptablesEgressState("-P OUTPUT ACCEPT\n"); got != "ready" {
		t.Fatalf("state=%q", got)
	}
	if got := iptablesEgressState("-P OUTPUT ACCEPT\n-A OUTPUT -j CUSTOM\n"); got != "unknown" {
		t.Fatalf("state=%q", got)
	}
}

func TestCloudFirewallIsWarningNotBlocker(t *testing.T) {
	response := networkPreflightResponse{CloudFirewallState: "unknown", Warnings: []string{"cloud_firewall_unverified"}, Blockers: []string{}}
	if response.CloudFirewallState != "unknown" || len(response.Warnings) != 1 || len(response.Blockers) != 0 {
		t.Fatalf("response=%+v", response)
	}
}
func TestNFTOutputInspectionIgnoresDropsOutsideOutputChain(t *testing.T) {
	rules := "table ip filter {\nchain INPUT { type filter hook input priority filter; policy accept; drop }\nchain OUTPUT { type filter hook output priority filter; policy accept; }\n}"
	if nftBlocksFalePaco(rules) {
		t.Fatal("input-chain drops must not be interpreted as egress blocks")
	}
}

func TestNFTOutputInspectionDetectsOutputDrops(t *testing.T) {
	rules := "table ip filter {\nchain OUTPUT { type filter hook output priority filter; policy accept;\nip daddr 177.11.49.223 tcp dport 5060 drop\n}\n}"
	if !nftBlocksFalePaco(rules) {
		t.Fatal("output-chain drop must be detected")
	}
}

func TestRTPRangeRequiresExactGeneralValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rtp.conf")
	if err := os.WriteFile(path, []byte("[general]\nrtpstart=10000\nrtpend=65000\n[other]\nrtpstart=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start, end, ok := readRTPRange(path)
	if !ok || start != 10000 || end != 65000 {
		t.Fatalf("range=%d:%d configured=%v", start, end, ok)
	}
}
func TestNetworkPreflightRequiresBearerBeforeProbing(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/falepaco/network/preflight", nil)
	(&server{}).networkPreflight(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
}
func TestSavedFalepacoUsesPersistedTransport(t *testing.T) {
	m := map[string]string{"FALEPACO_SIP_DOMAIN": "98034.falepaco.com.br", "FALEPACO_SIP_OUTBOUND_HOST": "96678.falepaco.com.br", "FALEPACO_SIP_OUTBOUND_PROXY": "98034.falepaco.com.br:5060", "FALEPACO_SIP_USERNAME": "100", "FALEPACO_SIP_EXTENSION": "100", "FALEPACO_SIP_PASSWORD": "secret", "FALEPACO_SIP_CALLER_ID": "551155200455", "FALEPACO_SIP_TRANSPORT": "udp", "FALEPACO_SIP_PORT": "5060"}
	req, err := savedFalepaco(m)
	if err != nil {
		t.Fatal(err)
	}
	if req.Transport != "udp" {
		t.Fatalf("transport=%s", req.Transport)
	}
	cfg, err := req.ToCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.Transport) != "udp" {
		t.Fatalf("canonical transport=%s", cfg.Transport)
	}
}

func TestAsteriskAuthReadbackParsesSemanticFields(t *testing.T) {
	output := "Auth:  trunk-falepaco-auth/100\n auth_type : userpass\n username : 100\n realm : 96678.falepaco.com.br\n password=[REDACTED]\n"
	if asteriskParameter(output, "auth_type") != "userpass" || asteriskParameter(output, "username") != "100" || asteriskParameter(output, "realm") != "96678.falepaco.com.br" {
		t.Fatal("auth readback fields were not parsed")
	}
}
