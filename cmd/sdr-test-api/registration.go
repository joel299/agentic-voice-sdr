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

type cappedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

func (b *cappedBuffer) Mark() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Len()
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func (b *cappedBuffer) StringFrom(mark int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := b.Buffer.String()
	if mark < 0 {
		mark = 0
	}
	if mark > len(value) {
		mark = len(value)
	}
	return value[mark:]
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

type registrationAttemptSteps struct {
	StartCapture func(context.Context) error
	Apply        func(context.Context) error
	Readback     func(context.Context) (bool, string, error)
	MarkWire     func() int
	Trigger      func(context.Context) (int, string)
	Wait         func(context.Context, string, bool) string
	StopCapture  func()
	ReadWire     func(int) sipWireEvidence
}

type registrationAttemptResult struct {
	Status, ErrorClass, TriggerErrorClass string
	PreState, PostState                   string
	TriggerAttempted, Triggered           bool
	TriggerExitCode                       int
	RegistrationObjectPresent             bool
	WireActivity                          bool
	Wire                                  sipWireEvidence
}

func runRegistrationAttempt(ctx context.Context, steps registrationAttemptSteps) registrationAttemptResult {
	result := registrationAttemptResult{Status: "LocalError", TriggerExitCode: -1}
	if err := steps.StartCapture(ctx); err != nil {
		result.ErrorClass = "wire_capture_start_failed"
		return result
	}
	captureActive := true
	wireMark := 0
	finishCapture := func() {
		if !captureActive {
			return
		}
		steps.StopCapture()
		captureActive = false
		result.Wire = steps.ReadWire(wireMark)
		result.WireActivity = result.Wire.Initial || result.Wire.FirstResponse != "" || result.Wire.FinalResponse != "" || result.Wire.Challenge || result.Wire.Authenticated
	}
	defer finishCapture()
	if err := steps.Apply(ctx); err != nil {
		result.Status, result.ErrorClass = "ApplyFailed", "registration_apply_failed"
		finishCapture()
		return result
	}
	present, state, err := steps.Readback(ctx)
	result.RegistrationObjectPresent = present
	if state == "" {
		state = "Unknown"
	}
	result.PreState = state
	if err != nil || !present {
		result.Status, result.ErrorClass = "LocalError", "registration_object_readback_failed"
		finishCapture()
		return result
	}
	wireMark = steps.MarkWire()
	result.TriggerAttempted = true
	result.TriggerExitCode, result.TriggerErrorClass = steps.Trigger(ctx)
	result.Triggered = result.TriggerErrorClass == "" && result.TriggerExitCode == 0
	result.PostState = steps.Wait(ctx, result.PreState, result.Triggered)
	if result.PostState == "" {
		result.PostState = "Unknown"
	}
	finishCapture()
	classified := classifyRegistrationAttempt(result.PreState, result.PostState, result.TriggerErrorClass, result.Wire)
	result.Status, result.ErrorClass = classified.Status, classified.ErrorClass
	return result
}

type registrationClassification struct{ Status, ErrorClass string }

func classifyRegistrationAttempt(preState, postState, triggerErrorClass string, wire sipWireEvidence) registrationClassification {
	if triggerErrorClass != "" {
		return registrationClassification{"TriggerFailed", triggerErrorClass}
	}
	activity := wire.Initial || wire.FirstResponse != "" || wire.FinalResponse != "" || wire.Challenge || wire.Authenticated
	if !activity {
		return registrationClassification{"NoWireActivity", "registration_trigger_no_wire"}
	}
	staleRejected := preState == "Rejected" && postState == "Rejected"
	if wire.FinalResponse == "200" && postState == "Registered" {
		return registrationClassification{"Registered", ""}
	}
	if wire.FinalResponse == "403" {
		return registrationClassification{"Rejected", "provider_registration_rejected"}
	}
	if wire.FinalResponse != "" && wire.FinalResponse != "200" && postState == "Rejected" && !staleRejected {
		return registrationClassification{"Rejected", "provider_registration_rejected"}
	}
	return registrationClassification{"Pending", "registration_attempt_incomplete"}
}

type asteriskCommandRunner func(context.Context, string) (string, int, error)

func sendAsteriskRegister(ctx context.Context, run asteriskCommandRunner) (int, string) {
	output, exitCode, err := run(ctx, "pjsip send register trunk-falepaco-reg")
	if err != nil || exitCode != 0 {
		return exitCode, "asterisk_cli_failed"
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "unable to") || strings.Contains(lower, "not found") || strings.Contains(lower, "no such") || strings.Contains(lower, "no registration") || strings.Contains(lower, "error") {
		return exitCode, "asterisk_cli_failed"
	}
	return exitCode, ""
}

func asteriskCommand(ctx context.Context, command string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "asterisk", "-rx", command)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(output), exitErr.ExitCode(), err
	}
	return string(output), -1, err
}

func callRegistrationReady(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), "Registered")
}

func shouldStopRegistrationPoll(preState, observedState string) bool {
	return observedState == "Registered" || (observedState == "Rejected" && preState != "Rejected")
}

func registrationStateFromOutput(output string) string {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "rejected") {
		return "Rejected"
	}
	if strings.Contains(lower, "unregistered") {
		return "Unregistered"
	}
	if regexp.MustCompile(`(?i)\bregistered\b`).MatchString(output) {
		return "Registered"
	}
	return "Unregistered"
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
	state := registrationStateFromOutput(string(out))
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
	if validation := falepacoSavedValidation(req, m); validation != nil {
		jsonOut(w, 400, map[string]any{"error": "configuration_invalid", "invalid_field": validation.Field, "validation_class": validation.Class, "secrets_redacted": true})
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
	var capture *exec.Cmd
	var captureOutput *cappedBuffer
	var applyErr error
	attempt := runRegistrationAttempt(ctx, registrationAttemptSteps{
		StartCapture: func(context.Context) error {
			var err error
			capture, captureOutput, err = startSIPWireCapture(req.Transport)
			return err
		},
		Apply: func(ctx context.Context) error {
			_, applyErr = mgr.ApplyTrunk(ctx, cfg)
			return applyErr
		},
		Readback: func(ctx context.Context) (bool, string, error) {
			out, err := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
			text := string(out)
			present := strings.Contains(text, "trunk-falepaco-reg") && !strings.Contains(text, "Unable to find object")
			return present, registrationStateFromOutput(text), err
		},
		MarkWire: func() int {
			if captureOutput == nil {
				return 0
			}
			return captureOutput.Mark()
		},
		Trigger: func(ctx context.Context) (int, string) { return sendAsteriskRegister(ctx, asteriskCommand) },
		Wait: func(ctx context.Context, preState string, triggered bool) string {
			if triggered {
				return waitRegistration(ctx, preState)
			}
			out, err := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
			if err != nil {
				return "Unknown"
			}
			return registrationStateFromOutput(string(out))
		},
		StopCapture: func() { stopSIPWireCapture(capture) },
		ReadWire: func(mark int) sipWireEvidence {
			if captureOutput == nil {
				return sipWireEvidence{}
			}
			return parseSIPWireCapture(captureOutput.StringFrom(mark), req.Auth.Username, m["FALEPACO_SIP_PASSWORD"])
		},
	})
	if attempt.Status == "LocalError" && attempt.ErrorClass == "wire_capture_start_failed" {
		jsonOut(w, 503, map[string]any{"error": "wire_capture_unavailable", "registration_capture_implemented": true, "registration_triggered": false, "registration_trigger_error_class": attempt.ErrorClass, "secrets_redacted": true})
		return
	}
	applyStage, applyClass, applySummary := "", "", ""
	if applyErr != nil {
		applyStage, applyClass, applySummary = safeApplyError(applyErr, m["FALEPACO_SIP_PASSWORD"])
	}
	status := 502
	if attempt.Status == "Registered" {
		status = 200
	}
	networkProtocolObserved := ""
	if attempt.Wire.Initial {
		networkProtocolObserved = strings.ToUpper(req.Transport)
	}
	jsonOut(w, status, map[string]any{
		"selected_transport": req.Transport, "network_protocol_observed": networkProtocolObserved,
		"registration_object_present": attempt.RegistrationObjectPresent,
		"registration_server_uri":     m["FALEPACO_SIP_REGISTRATION_SERVER_URI"], "registration_client_uri": m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"],
		"registration_contact_user": m["FALEPACO_SIP_CONTACT_USER"], "registration_outbound_proxy": req.OutboundProxy,
		"registration_status": attempt.Status, "registration_state": attempt.PostState,
		"registration_apply_error": applyErr != nil, "apply_stage": applyStage, "apply_error_class": applyClass, "apply_error_summary": applySummary,
		"registration_pre_state": attempt.PreState, "registration_triggered": attempt.Triggered, "registration_trigger_attempted": attempt.TriggerAttempted,
		"registration_trigger_command": "pjsip send register trunk-falepaco-reg", "registration_trigger_exit_code": attempt.TriggerExitCode,
		"registration_trigger_error_class": attempt.TriggerErrorClass, "registration_post_state": attempt.PostState,
		"registration_wire_activity_present": attempt.WireActivity, "registration_error_class": attempt.ErrorClass,
		"registration_capture_implemented": true, "registration_initial_request_present": attempt.Wire.Initial,
		"registration_first_response": attempt.Wire.FirstResponse, "registration_first_reason": attempt.Wire.FirstReason,
		"registration_challenge_received": attempt.Wire.Challenge, "registration_challenge_type": attempt.Wire.ChallengeType,
		"registration_realm": attempt.Wire.Realm, "registration_algorithm": attempt.Wire.Algorithm, "registration_qop": attempt.Wire.QOP,
		"registration_authenticated_request_sent": attempt.Wire.Authenticated, "registration_auth_username": attempt.Wire.AuthUsername,
		"registration_auth_realm": attempt.Wire.AuthRealm, "registration_auth_uri": attempt.Wire.AuthURI,
		"registration_digest_matches_runtime_secret": attempt.Wire.DigestMatches, "registration_final_response": attempt.Wire.FinalResponse,
		"registration_final_reason": attempt.Wire.FinalReason, "provider_server_or_user_agent": attempt.Wire.Server, "secrets_redacted": true,
	})
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

func waitRegistration(ctx context.Context, preState string) string {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(750 * time.Millisecond)
	defer poll.Stop()
	state := "Unregistered"
	for {
		cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		out, err := exec.CommandContext(cmdCtx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
		cancel()
		if err == nil {
			state = registrationStateFromOutput(string(out))
		}
		if shouldStopRegistrationPoll(preState, state) {
			return state
		}
		select {
		case <-ctx.Done():
			return state
		case <-deadline.C:
			return state
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
