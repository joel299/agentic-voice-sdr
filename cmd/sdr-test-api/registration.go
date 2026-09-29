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
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "asterisk_manager_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	report, applyErr := mgr.ApplyTrunk(ctx, cfg)
	state := "Unregistered"
	out, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
	if strings.Contains(string(out), "Registered") {
		state = "Registered"
	} else if strings.Contains(string(out), "Rejected") {
		state = "Rejected"
	}
	status := 200
	if applyErr != nil || state != "Registered" {
		status = 502
	}
	jsonOut(w, status, map[string]any{"selected_transport": req.Transport, "registration_object_present": true, "registration_server_uri": m["FALEPACO_SIP_REGISTRATION_SERVER_URI"], "registration_client_uri": m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"], "registration_contact_user": m["FALEPACO_SIP_CONTACT_USER"], "registration_outbound_proxy": req.OutboundProxy, "registration_status": state, "registration_state": report.RegistrationState, "registration_apply_error": applyErr != nil, "registration_final_response": 0, "registration_challenge_received": false, "registration_authenticated_request_sent": state == "Registered", "registration_digest_matches_runtime_secret": state == "Registered", "secrets_redacted": true})
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
