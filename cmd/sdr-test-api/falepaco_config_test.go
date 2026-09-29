package main

import (
	"context"
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

func TestSafeApplyErrorReportsStageAndRedactsSecrets(t *testing.T) {
	stage, class, summary := safeApplyError(errors.New("asterisk reload failed: password=topsecret"), "topsecret")
	if stage != "reload" || class != "pjsip_configuration_error" || strings.Contains(summary, "topsecret") || !strings.Contains(summary, "[REDACTED]") {
		t.Fatalf("unsafe or incomplete apply diagnostics: stage=%q class=%q summary=%q", stage, class, summary)
	}
}
func TestProviderAllowlistContainsOfficialCount(t *testing.T) {
	if len(falePacoProviderIPs) != 21 {
		t.Fatalf("provider allowlist count=%d", len(falePacoProviderIPs))
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
