package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
)

type asteriskReadFunc func(context.Context, string) (string, error)

func callDestinationAllowed(destination string) bool { return destination == allowedDestination }

func runAsteriskCommand(ctx context.Context, command string) (string, error) {
	output, err := exec.CommandContext(ctx, "asterisk", "-rx", command).CombinedOutput()
	return string(output), err
}

func canonicalFalePacoProfile(req httpapi.SIPConfigRequest, env map[string]string) bool {
	const canonicalHost = "98034.falepaco.com.br"
	return strings.EqualFold(env["FALEPACO_SIP_DOMAIN"], canonicalHost) &&
		strings.EqualFold(env["FALEPACO_SIP_OUTBOUND_HOST"], canonicalHost) &&
		strings.EqualFold(req.Host, canonicalHost) &&
		strings.EqualFold(req.Registrar, canonicalHost) &&
		req.OutboundProxy == canonicalHost+":5060" && req.Port == 5060 && req.Transport == "tcp" &&
		req.Auth.Type == "userpass" && req.Auth.Username == "100" && req.FromUser == "100" && req.FromDomain == canonicalHost && req.CallerID == "551155200455" &&
		req.RegistrationRequired &&
		req.RegistrationServerURI == "sip:"+canonicalHost+":5060" &&
		req.RegistrationClientURI == "sip:100@"+canonicalHost+":5060" &&
		req.RegistrationContactUser == "100" && strings.EqualFold(req.RegistrationRealm, canonicalHost) &&
		req.RegistrationRetryInterval == 60 && req.RegistrationMaxRetries == 3
}

func activePJSIPProfileStatus(ctx context.Context, req httpapi.SIPConfigRequest, read asteriskReadFunc) (string, string) {
	registration, err := read(ctx, "pjsip show registration trunk-falepaco-reg")
	if err != nil {
		return "Unknown", "registration_readback_failed"
	}
	state := registrationStateFromOutput(registration)
	if !callRegistrationReady(state) {
		return state, ""
	}

	endpoint, err := read(ctx, "pjsip show endpoint trunk-falepaco")
	if err != nil {
		return state, "endpoint_readback_failed"
	}
	aor, err := read(ctx, "pjsip show aor trunk-falepaco-aor")
	if err != nil {
		return state, "aor_readback_failed"
	}
	auth, err := read(ctx, "pjsip show auth trunk-falepaco-auth")
	if err != nil {
		return state, "auth_readback_failed"
	}
	transport, err := read(ctx, "pjsip show transport transport-tcp")
	if err != nil {
		return state, "transport_readback_failed"
	}
	if !strings.Contains(endpoint, "trunk-falepaco") || !strings.Contains(aor, "trunk-falepaco-aor") || !strings.Contains(auth, "trunk-falepaco-auth") {
		return state, "active_object_missing"
	}
	contact := "sip:" + req.Host + ":" + fmt.Sprint(req.Port) + ";transport=" + req.Transport
	proxy := "sip:" + req.OutboundProxy + ";transport=" + req.Transport + ";lr"
	checks := []struct{ output, field, want string }{
		{endpoint, "aors", "trunk-falepaco-aor"},
		{endpoint, "outbound_auth", "trunk-falepaco-auth"},
		{endpoint, "callerid", req.CallerID},
		{endpoint, "from_user", req.FromUser},
		{endpoint, "from_domain", req.Host},
		{aor, "contact", contact},
		{auth, "auth_type", "userpass"},
		{auth, "username", req.Auth.Username},
		{auth, "realm", req.RegistrationRealm},
		{registration, "server_uri", req.RegistrationServerURI},
		{registration, "client_uri", req.RegistrationClientURI},
		{registration, "contact_user", req.RegistrationContactUser},
		{registration, "outbound_proxy", proxy},
		{registration, "transport", "transport-tcp"},
		{transport, "protocol", "tcp"},
	}
	for _, check := range checks {
		if !strings.EqualFold(normalizePJSIPValue(asteriskParameter(check.output, check.field)), normalizePJSIPValue(check.want)) {
			return state, check.field + "_mismatch"
		}
	}
	return state, ""
}

func normalizePJSIPValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, `\;`, ";")
	return strings.Trim(value, "<>\" ")
}

func startSIPCallPCAPCapture(providerIPs []string) (*exec.Cmd, string, error) {
	if _, err := exec.LookPath("tcpdump"); err != nil {
		return nil, "", err
	}
	hostFilters := make([]string, 0, len(providerIPs))
	for _, value := range providerIPs {
		ip := net.ParseIP(value)
		if ip == nil {
			return nil, "", fmt.Errorf("invalid provider address")
		}
		hostFilters = append(hostFilters, "host "+ip.String())
	}
	if len(hostFilters) == 0 {
		return nil, "", fmt.Errorf("provider addresses missing")
	}
	file, err := os.CreateTemp(os.TempDir(), "gru142-call-*.pcap")
	if err != nil {
		return nil, "", err
	}
	path := file.Name()
	if err = file.Chmod(0600); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, "", err
	}
	filter := "(" + strings.Join(hostFilters, " or ") + ") and (port 5060 or (udp and portrange 10000-65000))"
	cmd := exec.Command("tcpdump", "-U", "-i", "any", "-nn", "-s0", "-c", fmt.Sprint(maxRegistrationTCPPackets), "-w", path, filter)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		_ = os.Remove(path)
		return nil, "", err
	}
	time.Sleep(150 * time.Millisecond)
	if err = cmd.Process.Signal(syscall.Signal(0)); err != nil {
		_ = cmd.Wait()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("capture process exited early")
	}
	return cmd, path, nil
}

func waitForCallCapture(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
