package main

import (
	"context"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

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
	req.RegistrationRequired = true
	cfg, e := req.ToCanonical()
	if e != nil {
		jsonOut(w, 400, map[string]string{"error": "configuration_invalid"})
		return
	}
	cfg.DeferRegistrationCheck = true
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "asterisk_manager_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	_, applyErr := mgr.ApplyTrunk(ctx, cfg)
	applyStage, applyClass, applySummary := "", "", ""
	if applyErr != nil {
		applyStage, applyClass, applySummary = safeApplyError(applyErr, m["FALEPACO_SIP_PASSWORD"])
	}
	state, output := waitRegistration(ctx)
	initialRequestPresent := strings.Contains(output, "Request Sent") || state == "Registered" || state == "Rejected"
	status := 200
	if applyErr != nil || state != "Registered" {
		status = 502
	}
	jsonOut(w, status, map[string]any{"selected_transport": req.Transport, "registration_object_present": strings.Contains(output, "trunk-falepaco-reg"), "registration_server_uri": m["FALEPACO_SIP_REGISTRATION_SERVER_URI"], "registration_client_uri": m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"], "registration_contact_user": m["FALEPACO_SIP_CONTACT_USER"], "registration_outbound_proxy": req.OutboundProxy, "registration_status": state, "registration_state": state, "registration_apply_error": applyErr != nil, "apply_stage": applyStage, "apply_error_class": applyClass, "apply_error_summary": applySummary, "registration_initial_request_present": initialRequestPresent, "registration_first_response": nil, "registration_challenge_received": nil, "registration_authenticated_request_sent": nil, "registration_digest_matches_runtime_secret": nil, "registration_final_response": nil, "secrets_redacted": true})
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
