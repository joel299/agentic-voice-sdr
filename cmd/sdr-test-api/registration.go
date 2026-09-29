package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type sipWireEvidence struct {
	Initial                              bool
	FirstResponse, FirstReason           string
	Challenge                            bool
	ChallengeType, Realm, Algorithm, QOP string
	Authenticated                        bool
	AuthUsername, AuthRealm, AuthURI     string
	DigestMatches                        *bool
	FinalResponse, FinalReason, Server   string
}

var digestValue = regexp.MustCompile(`(?i)(cnonce|username|algorithm|response|realm|nonce|uri|qop|nc)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

func parseDigestFields(line string) map[string]string {
	fields := map[string]string{}
	for _, match := range digestValue.FindAllStringSubmatch(line, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		fields[strings.ToLower(match[1])] = value
	}
	return fields
}

func digestMatchesSIP(username, realm, secret, method string, fields map[string]string) bool {
	if fields["response"] == "" || fields["nonce"] == "" || fields["uri"] == "" {
		return false
	}
	algorithm := strings.ToUpper(fields["algorithm"])
	if algorithm != "" && algorithm != "MD5" && algorithm != "MD5-SESS" {
		return false
	}
	md5hex := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	ha1 := md5hex(username + ":" + realm + ":" + secret)
	if algorithm == "MD5-SESS" {
		if fields["cnonce"] == "" {
			return false
		}
		ha1 = md5hex(ha1 + ":" + fields["nonce"] + ":" + fields["cnonce"])
	}
	ha2 := md5hex(method + ":" + fields["uri"])
	qop := fields["qop"]
	var expected string
	if qop == "" {
		expected = md5hex(ha1 + ":" + fields["nonce"] + ":" + ha2)
	} else {
		if !strings.EqualFold(qop, "auth") {
			return false
		}
		expected = md5hex(ha1 + ":" + fields["nonce"] + ":" + fields["nc"] + ":" + fields["cnonce"] + ":" + qop + ":" + ha2)
	}
	return strings.EqualFold(expected, fields["response"])
}

func parseSIPWireCapture(raw, username, secret string) sipWireEvidence {
	var ev sipWireEvidence
	var firstSet bool
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "REGISTER ") {
			ev.Initial = true
			continue
		}
		if strings.HasPrefix(line, "SIP/2.0 ") {
			parts := strings.SplitN(strings.TrimPrefix(line, "SIP/2.0 "), " ", 2)
			code := parts[0]
			reason := ""
			if len(parts) > 1 {
				reason = parts[1]
			}
			if !firstSet {
				ev.FirstResponse, ev.FirstReason, firstSet = code, reason, true
			}
			if code != "401" && code != "407" {
				ev.FinalResponse, ev.FinalReason = code, reason
			}
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "www-authenticate:") || strings.HasPrefix(lower, "proxy-authenticate:") {
			ev.Challenge = true
			ev.ChallengeType = "401"
			if strings.HasPrefix(lower, "proxy-authenticate:") {
				ev.ChallengeType = "407"
			}
			f := parseDigestFields(line)
			ev.Realm, ev.Algorithm, ev.QOP = f["realm"], f["algorithm"], f["qop"]
		}
		if strings.HasPrefix(lower, "authorization:") || strings.HasPrefix(lower, "proxy-authorization:") {
			ev.Authenticated = true
			f := parseDigestFields(line)
			ev.AuthUsername, ev.AuthRealm, ev.AuthURI = f["username"], f["realm"], f["uri"]
			ok := digestMatchesSIP(ev.AuthUsername, ev.AuthRealm, secret, "REGISTER", f)
			ev.DigestMatches = &ok
		}
		if strings.HasPrefix(lower, "server:") || strings.HasPrefix(lower, "user-agent:") {
			ev.Server = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return ev
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len() >= 2<<20 {
		return len(p), nil
	}
	n := len(p)
	if n > (2<<20)-b.Len() {
		p = p[:(2<<20)-b.Len()]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

var registrationTestMu sync.Mutex

type registrationResponse struct {
	Configured                bool   `json:"configured"`
	RegistrationObjectPresent bool   `json:"registration_object_present"`
	SelectedTransport         string `json:"selected_transport"`
	RegistrationServerURI     string `json:"registration_server_uri"`
	RegistrationClientURI     string `json:"registration_client_uri"`
	RegistrationContactUser   string `json:"registration_contact_user"`
	RegistrationOutboundProxy string `json:"registration_outbound_proxy"`
	RegistrationStatus        string `json:"registration_status"`
	RegistrationState         string `json:"registration_state"`
	SecretsRedacted           bool   `json:"secrets_redacted"`
}

func (s *server) registrationGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	m, e := dotenv(sipEnv)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	req, e := savedFalepaco(m)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "configuration_invalid"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
	all, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registrations").CombinedOutput()
	present := strings.Contains(string(out), "trunk-falepaco-reg") || strings.Contains(string(all), "trunk-falepaco-reg")
	state := "Unregistered"
	if strings.Contains(string(out), "Registered") {
		state = "Registered"
	} else if strings.Contains(string(out), "Rejected") {
		state = "Rejected"
	}
	jsonOut(w, 200, registrationResponse{Configured: m["FALEPACO_SIP_USERNAME"] != "" && m["FALEPACO_SIP_PASSWORD"] != "", RegistrationObjectPresent: present, SelectedTransport: req.Transport, RegistrationServerURI: m["FALEPACO_SIP_REGISTRATION_SERVER_URI"], RegistrationClientURI: m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"], RegistrationContactUser: m["FALEPACO_SIP_CONTACT_USER"], RegistrationOutboundProxy: req.OutboundProxy, RegistrationStatus: state, RegistrationState: state, SecretsRedacted: true})
}
func (s *server) registrationTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	registrationTestMu.Lock()
	defer registrationTestMu.Unlock()
	m, e := dotenv(sipEnv)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	req, e := savedFalepaco(m)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "configuration_invalid"})
		return
	}
	if !req.RegistrationRequired {
		jsonOut(w, 400, map[string]any{"error": "invalid_registration_configuration", "apply_error_summary": "registration.enabled must be true before REGISTER testing", "secrets_redacted": true})
		return
	}
	if (req.Transport != "tcp" && req.Transport != "udp") || req.Port != 5060 {
		jsonOut(w, 400, map[string]any{"error": "invalid_registration_configuration", "apply_error_summary": "REGISTER test requires configured TCP or UDP transport on port 5060", "secrets_redacted": true})
		return
	}
	cfg, e := req.ToCanonical()
	if e != nil {
		jsonOut(w, 400, map[string]string{"error": "configuration_invalid"})
		return
	}
	cfg, field, class, summary := pinFalePacoTrunkConfig(cfg, m["FALEPACO_SIP_DOMAIN"], m["FALEPACO_SIP_OUTBOUND_HOST"], req.OutboundProxy, 4*time.Second)
	if class != "" {
		jsonOut(w, 502, map[string]any{"error": "registration_test_failed", "apply_stage": "dns", "apply_error_class": class, "apply_error_field": field, "apply_error_summary": summary, "secrets_redacted": true})
		return
	}
	cfg.DeferRegistrationCheck = true
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "asterisk_manager_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	capture, captureOutput, captureErr := startSIPWireCapture(req.Transport)
	if captureErr != nil {
		jsonOut(w, 503, map[string]any{"error": "wire_capture_unavailable", "registration_capture_implemented": true, "secrets_redacted": true})
		return
	}
	_, applyErr := mgr.ApplyTrunk(ctx, cfg)
	applyStage, applyClass, applySummary := "", "", ""
	if applyErr != nil {
		applyStage, applyClass, applySummary = safeApplyError(applyErr, m["FALEPACO_SIP_PASSWORD"])
	}
	state := "ApplyFailed"
	if applyErr == nil {
		state, _ = waitRegistration(ctx)
	}
	registrationReadback, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
	registrationObjectPresent := strings.Contains(string(registrationReadback), "trunk-falepaco-reg") && !strings.Contains(string(registrationReadback), "Unable to find object")
	stopSIPWireCapture(capture)
	wire := parseSIPWireCapture(captureOutput.String(), req.Auth.Username, m["FALEPACO_SIP_PASSWORD"])
	if wire.FinalResponse == "200" {
		state = "Registered"
	} else if wire.FinalResponse == "403" {
		state = "Rejected"
	}
	status := 200
	if applyErr != nil || wire.FinalResponse != "200" {
		status = 502
	}
	networkProtocolObserved := ""
	if wire.Initial {
		networkProtocolObserved = strings.ToUpper(req.Transport)
	}
	jsonOut(w, status, map[string]any{"selected_transport": req.Transport, "network_protocol_observed": networkProtocolObserved, "registration_object_present": registrationObjectPresent, "registration_server_uri": m["FALEPACO_SIP_REGISTRATION_SERVER_URI"], "registration_client_uri": m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"], "registration_contact_user": m["FALEPACO_SIP_CONTACT_USER"], "registration_outbound_proxy": req.OutboundProxy, "registration_status": state, "registration_state": state, "registration_apply_error": applyErr != nil, "apply_stage": applyStage, "apply_error_class": applyClass, "apply_error_summary": applySummary,
		"registration_capture_implemented": true, "registration_initial_request_present": wire.Initial,
		"registration_first_response": wire.FirstResponse, "registration_first_reason": wire.FirstReason,
		"registration_challenge_received": wire.Challenge, "registration_challenge_type": wire.ChallengeType,
		"registration_realm": wire.Realm, "registration_algorithm": wire.Algorithm, "registration_qop": wire.QOP,
		"registration_authenticated_request_sent": wire.Authenticated, "registration_auth_username": wire.AuthUsername,
		"registration_auth_realm": wire.AuthRealm, "registration_auth_uri": wire.AuthURI,
		"registration_digest_matches_runtime_secret": wire.DigestMatches, "registration_final_response": wire.FinalResponse,
		"registration_final_reason": wire.FinalReason, "provider_server_or_user_agent": wire.Server, "secrets_redacted": true})
}

func startSIPWireCapture(selectedTransport string) (*exec.Cmd, *cappedBuffer, error) {
	if selectedTransport != "tcp" && selectedTransport != "udp" {
		return nil, nil, errors.New("unsupported capture transport")
	}
	if _, err := exec.LookPath("tcpdump"); err != nil {
		return nil, nil, err
	}
	protocol := selectedTransport
	cmd := exec.Command("tcpdump", "-i", "any", "-l", "-nn", "-s0", "-A", protocol, "port", "5060")
	buffer := &cappedBuffer{}
	cmd.Stdout, cmd.Stderr = buffer, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	time.Sleep(150 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return nil, nil, errors.New("tcpdump exited before capture")
	}
	return cmd, buffer, nil
}

func stopSIPWireCapture(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func waitRegistration(ctx context.Context) (string, string) {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(750 * time.Millisecond)
	defer poll.Stop()
	state, output := "Unregistered", ""
	for {
		cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		out, _ := exec.CommandContext(cmdCtx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
		cancel()
		output = string(out)
		switch {
		case strings.Contains(output, "Rejected") || strings.Contains(output, "REJECTED"):
			return "Rejected", output
		case strings.Contains(output, "Registered") || strings.Contains(output, "REGISTERED"):
			return "Registered", output
		}
		select {
		case <-ctx.Done():
			return state, output
		case <-deadline.C:
			return state, output
		case <-poll.C:
		}
	}
}
func (s *server) registrationDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	m, e := dotenv(sipEnv)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	m["FALEPACO_SIP_REGISTRATION_REQUIRED"] = "false"
	m["FALEPACO_SIP_REGISTRATION_ENABLED"] = "false"
	if e = atomicDotenvWrite(m); e != nil {
		jsonOut(w, 500, map[string]string{"error": "credential_write_failed"})
		return
	}
	req, e := savedFalepaco(m)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "configuration_invalid"})
		return
	}
	cfg, e := req.ToCanonical()
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "configuration_invalid"})
		return
	}
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "asterisk_manager_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, e = mgr.ApplyTrunk(ctx, cfg); e != nil {
		jsonOut(w, 502, map[string]any{"error": "registration_remove_apply_failed", "removed": false, "secrets_redacted": true})
		return
	}
	jsonOut(w, 200, map[string]any{"removed": true, "secrets_redacted": true})
}
