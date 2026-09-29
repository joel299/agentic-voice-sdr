package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

type falepacoConfigRequest struct {
	ProviderAddress      string             `json:"provider_address"`
	RequestURIHost       string             `json:"request_uri_host"`
	OutboundProxy        string             `json:"outbound_proxy"`
	Username             string             `json:"username"`
	Extension            string             `json:"extension"`
	Password             *string            `json:"password,omitempty"`
	CallerID             string             `json:"caller_id"`
	Transport            string             `json:"transport"`
	Port                 int                `json:"port"`
	RegistrationRequired *bool              `json:"registration_required,omitempty"`
	Registration         RegistrationConfig `json:"registration,omitempty"`
}
type RegistrationConfig struct {
	Enabled              bool   `json:"enabled"`
	ServerURI            string `json:"server_uri"`
	ClientURI            string `json:"client_uri"`
	ContactUser          string `json:"contact_user"`
	Realm                string `json:"realm"`
	RetryIntervalSeconds int    `json:"retry_interval_seconds"`
	MaxRetries           int    `json:"max_retries"`
}
type falepacoConfigResponse struct {
	Configured         bool               `json:"configured"`
	ProviderAddress    string             `json:"provider_address"`
	RequestURIHost     string             `json:"request_uri_host"`
	OutboundProxy      string             `json:"outbound_proxy"`
	Username           string             `json:"username"`
	Extension          string             `json:"extension"`
	PasswordConfigured bool               `json:"password_configured"`
	CallerID           string             `json:"caller_id"`
	Transport          string             `json:"transport"`
	Port               int                `json:"port"`
	Registration       RegistrationConfig `json:"registration"`
}

func validSIPHost(h string) error {
	h = strings.TrimSpace(h)
	if h == "" || strings.ContainsAny(h, " /\t\r\n") {
		return errors.New("invalid SIP host")
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified()) {
		return errors.New("internal SIP host")
	}
	ips, e := net.LookupIP(h)
	if e != nil || len(ips) == 0 {
		return errors.New("SIP host does not resolve")
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
			return errors.New("SIP host resolves internal")
		}
	}
	return nil
}
func atomicDotenvWrite(m map[string]string) error { return atomicDotenvWriteAt(sipEnv, m) }

func atomicDotenvWriteAt(path string, m map[string]string) error {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".falepaco.env.tmp-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e != nil {
		return e
	}
	for _, k := range []string{"FALEPACO_SIP_DOMAIN", "FALEPACO_SIP_OUTBOUND_HOST", "FALEPACO_SIP_OUTBOUND_PROXY", "FALEPACO_SIP_USERNAME", "FALEPACO_SIP_EXTENSION", "FALEPACO_SIP_PASSWORD", "FALEPACO_SIP_CALLER_ID", "FALEPACO_SIP_TRANSPORT", "FALEPACO_SIP_PORT", "FALEPACO_SIP_REGISTRATION_REQUIRED", "FALEPACO_SIP_REGISTRATION_ENABLED", "FALEPACO_SIP_REGISTRATION_SERVER_URI", "FALEPACO_SIP_REGISTRATION_CLIENT_URI", "FALEPACO_SIP_CONTACT_USER", "FALEPACO_SIP_REALM", "FALEPACO_SIP_RETRY_INTERVAL", "FALEPACO_SIP_MAX_RETRIES"} {
		if _, ok := m[k]; ok {
			if _, e = fmt.Fprintf(f, "%s=%s\n", k, m[k]); e != nil {
				return e
			}
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return e
}
func savedFalepaco(m map[string]string) (httpapi.SIPConfigRequest, error) {
	domain := m["FALEPACO_SIP_DOMAIN"]
	host := m["FALEPACO_SIP_OUTBOUND_HOST"]
	if host == "" {
		host = domain
	}
	port := 5060
	if m["FALEPACO_SIP_PORT"] != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(m["FALEPACO_SIP_PORT"]))
		if err != nil || parsed <= 0 {
			return httpapi.SIPConfigRequest{}, errors.New("invalid SIP port")
		}
		port = parsed
	}
	transport := m["FALEPACO_SIP_TRANSPORT"]
	if transport == "" {
		transport = "tcp"
	}
	parseBool := func(key string) (bool, bool, error) {
		value, exists := m[key]
		if !exists || value == "" {
			return false, false, nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, true, fmt.Errorf("invalid_registration_configuration: %s must be boolean", key)
		}
		return parsed, true, nil
	}
	reg, enabledPresent, err := parseBool("FALEPACO_SIP_REGISTRATION_ENABLED")
	if err != nil {
		return httpapi.SIPConfigRequest{}, err
	}
	legacy, legacyPresent, err := parseBool("FALEPACO_SIP_REGISTRATION_REQUIRED")
	if err != nil {
		return httpapi.SIPConfigRequest{}, err
	}
	if enabledPresent && legacyPresent && reg != legacy {
		return httpapi.SIPConfigRequest{}, errors.New("invalid_registration_configuration: conflicting registration flags")
	}
	if !enabledPresent {
		reg = legacy
	}
	parsePositive := func(key string, fallback int) (int, error) {
		value := strings.TrimSpace(m[key])
		if value == "" {
			return fallback, nil
		}
		result, err := strconv.Atoi(value)
		if err != nil || result <= 0 {
			return 0, fmt.Errorf("invalid_registration_configuration: %s must be a positive integer", key)
		}
		return result, nil
	}
	retry, err := parsePositive("FALEPACO_SIP_RETRY_INTERVAL", 60)
	if err != nil {
		return httpapi.SIPConfigRequest{}, err
	}
	maxRetries, err := parsePositive("FALEPACO_SIP_MAX_RETRIES", 3)
	if err != nil {
		return httpapi.SIPConfigRequest{}, err
	}
	req := httpapi.SIPConfigRequest{Provider: "falepaco", Name: "falepaco", Host: host, Port: port, Transport: transport, Registrar: host, OutboundProxy: m["FALEPACO_SIP_OUTBOUND_PROXY"], FromDomain: host, FromUser: m["FALEPACO_SIP_EXTENSION"], CallerID: m["FALEPACO_SIP_CALLER_ID"], SendPAI: true, Auth: httpapi.SIPAuthRequest{Type: "userpass", Username: m["FALEPACO_SIP_USERNAME"], Secret: m["FALEPACO_SIP_PASSWORD"]}, RegistrationRequired: reg, Enabled: true}
	req.RegistrationServerURI = m["FALEPACO_SIP_REGISTRATION_SERVER_URI"]
	req.RegistrationClientURI = m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"]
	req.RegistrationContactUser = m["FALEPACO_SIP_CONTACT_USER"]
	req.RegistrationRealm = m["FALEPACO_SIP_REALM"]
	req.RegistrationRetryInterval = retry
	req.RegistrationMaxRetries = maxRetries
	return req, nil
}
func (s *server) falepacoGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	m, err := dotenv(sipEnv)
	if err != nil && !os.IsNotExist(err) {
		jsonOut(w, 500, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	req, err := savedFalepaco(m)
	if err != nil {
		jsonOut(w, 500, map[string]string{"error": "invalid_registration_configuration"})
		return
	}
	jsonOut(w, 200, falepacoConfigResponse{Configured: m["FALEPACO_SIP_PASSWORD"] != "" && m["FALEPACO_SIP_USERNAME"] != "", ProviderAddress: m["FALEPACO_SIP_DOMAIN"], RequestURIHost: m["FALEPACO_SIP_OUTBOUND_HOST"], OutboundProxy: req.OutboundProxy, Username: req.Auth.Username, Extension: req.FromUser, PasswordConfigured: m["FALEPACO_SIP_PASSWORD"] != "", CallerID: req.CallerID, Transport: req.Transport, Port: req.Port, Registration: RegistrationConfig{Enabled: req.RegistrationRequired, ServerURI: req.RegistrationServerURI, ClientURI: req.RegistrationClientURI, ContactUser: req.RegistrationContactUser, Realm: req.RegistrationRealm, RetryIntervalSeconds: req.RegistrationRetryInterval, MaxRetries: req.RegistrationMaxRetries}})
}
func falepacoEnv(in falepacoConfigRequest, password string) map[string]string {
	return map[string]string{"FALEPACO_SIP_DOMAIN": in.ProviderAddress, "FALEPACO_SIP_OUTBOUND_HOST": in.RequestURIHost, "FALEPACO_SIP_OUTBOUND_PROXY": in.OutboundProxy, "FALEPACO_SIP_USERNAME": in.Username, "FALEPACO_SIP_EXTENSION": in.Extension, "FALEPACO_SIP_PASSWORD": password, "FALEPACO_SIP_CALLER_ID": in.CallerID, "FALEPACO_SIP_TRANSPORT": in.Transport, "FALEPACO_SIP_PORT": fmt.Sprint(in.Port), "FALEPACO_SIP_REGISTRATION_REQUIRED": fmt.Sprint(in.Registration.Enabled), "FALEPACO_SIP_REGISTRATION_ENABLED": fmt.Sprint(in.Registration.Enabled), "FALEPACO_SIP_REGISTRATION_SERVER_URI": in.Registration.ServerURI, "FALEPACO_SIP_REGISTRATION_CLIENT_URI": in.Registration.ClientURI, "FALEPACO_SIP_CONTACT_USER": in.Registration.ContactUser, "FALEPACO_SIP_REALM": in.Registration.Realm, "FALEPACO_SIP_RETRY_INTERVAL": fmt.Sprint(in.Registration.RetryIntervalSeconds), "FALEPACO_SIP_MAX_RETRIES": fmt.Sprint(in.Registration.MaxRetries)}
}

func (s *server) falepacoPut(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in falepacoConfigRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		jsonOut(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	if in.RegistrationRequired != nil && *in.RegistrationRequired != in.Registration.Enabled {
		jsonOut(w, 400, map[string]string{"error": "invalid_registration_configuration"})
		return
	}
	if in.Registration.RetryIntervalSeconds == 0 {
		in.Registration.RetryIntervalSeconds = 60
	}
	if in.Registration.MaxRetries == 0 {
		in.Registration.MaxRetries = 3
	}
	if in.Registration.RetryIntervalSeconds < 1 || in.Registration.MaxRetries < 1 {
		jsonOut(w, 400, map[string]string{"error": "invalid_registration_configuration"})
		return
	}
	if in.Registration.Enabled {
		if in.Registration.ServerURI == "" || in.Registration.ClientURI == "" || in.Registration.ContactUser == "" || in.Registration.Realm == "" {
			jsonOut(w, 400, map[string]string{"error": "invalid_registration_configuration"})
			return
		}
	}
	old, err := dotenv(sipEnv)
	if err != nil && !os.IsNotExist(err) {
		jsonOut(w, 500, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	password := old["FALEPACO_SIP_PASSWORD"]
	if in.Password != nil {
		if *in.Password == "" {
			jsonOut(w, 400, map[string]string{"error": "password_must_not_be_empty"})
			return
		}
		password = *in.Password
	}
	if in.ProviderAddress == "" || in.RequestURIHost == "" || in.OutboundProxy == "" || in.Username == "" || in.Extension == "" || password == "" || in.CallerID == "" || (strings.ToLower(in.Transport) != "tcp" && strings.ToLower(in.Transport) != "udp") || in.Port != 5060 {
		jsonOut(w, 400, map[string]string{"error": "invalid_configuration"})
		return
	}
	in.Transport = strings.ToLower(in.Transport)
	for _, h := range []string{in.ProviderAddress, in.RequestURIHost, strings.Split(in.OutboundProxy, ":")[0]} {
		if e := validSIPHost(h); e != nil {
			jsonOut(w, 400, map[string]string{"error": "invalid_sip_host"})
			return
		}
	}
	m := falepacoEnv(in, password)
	if e := atomicDotenvWrite(m); e != nil {
		jsonOut(w, 500, map[string]string{"error": "credential_write_failed"})
		return
	}
	s.falepacoGet(w, r)
}
func asteriskParameter(output, name string) string {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == name {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func (s *server) falepacoApply(w http.ResponseWriter, r *http.Request) {
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
		stage, class, summary := safeApplyError(e, m["FALEPACO_SIP_PASSWORD"])
		jsonOut(w, 502, map[string]any{"error": "configuration_invalid", "apply_stage": stage, "apply_error_class": class, "apply_error_summary": summary, "secrets_redacted": true})
		return
	}
	cfg, e := req.ToCanonical()
	if e != nil {
		stage, class, summary := safeApplyError(e, m["FALEPACO_SIP_PASSWORD"])
		jsonOut(w, 400, map[string]any{"error": "configuration_invalid", "apply_stage": stage, "apply_error_class": class, "apply_error_summary": summary, "secrets_redacted": true})
		return
	}
	cfg.DeferRegistrationCheck = true
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]any{"error": "asterisk_manager_unavailable", "apply_stage": "stage", "apply_error_class": "asterisk_manager_unavailable", "apply_error_summary": "Asterisk manager could not be initialized", "secrets_redacted": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, e := mgr.ApplyTrunk(ctx, cfg)
	if e != nil {
		stage, class, summary := safeApplyError(e, m["FALEPACO_SIP_PASSWORD"])
		jsonOut(w, 502, map[string]any{"error": "sip_apply_failed", "apply_stage": stage, "apply_error_class": class, "apply_error_summary": summary, "secrets_redacted": true})
		return
	}
	out, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show endpoint trunk-falepaco").CombinedOutput()
	if !strings.Contains(string(out), "trunk-falepaco") {
		jsonOut(w, 502, map[string]any{"error": "asterisk_readback_failed", "apply_stage": "endpoint", "apply_error_class": "pjsip_endpoint_readback_failed", "apply_error_summary": "trunk endpoint missing from Asterisk readback", "secrets_redacted": true})
		return
	}
	if !strings.Contains(string(out), "trunk-falepaco-auth") {
		jsonOut(w, 502, map[string]any{"error": "asterisk_auth_readback_failed", "apply_stage": "auth", "apply_error_class": "pjsip_auth_readback_failed", "apply_error_summary": "configured auth object missing from endpoint readback", "secrets_redacted": true})
		return
	}
	authOut, authErr := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show auth trunk-falepaco-auth").CombinedOutput()
	authText := string(authOut)
	authPresent := authErr == nil && strings.Contains(authText, "Auth:  trunk-falepaco-auth/"+req.Auth.Username) && asteriskParameter(authText, "auth_type") == "userpass" && asteriskParameter(authText, "username") == req.Auth.Username && asteriskParameter(authText, "realm") == req.RegistrationRealm
	if !authPresent {
		jsonOut(w, 502, map[string]any{"error": "asterisk_auth_readback_failed", "apply_stage": "auth", "apply_error_class": "pjsip_auth_readback_mismatch", "apply_error_summary": "auth object username, type or realm did not match configured values", "secrets_redacted": true})
		return
	}
	transportName := "transport-" + req.Transport
	transportOut, transportErr := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show transport "+transportName).CombinedOutput()
	if transportErr != nil || !strings.Contains(string(transportOut), transportName) {
		jsonOut(w, 502, map[string]any{"error": "asterisk_transport_readback_failed", "apply_stage": "transport", "apply_error_class": "pjsip_transport_readback_failed", "apply_error_summary": "configured Asterisk transport missing from readback", "secrets_redacted": true})
		return
	}
	registrationPresent := false
	if req.RegistrationRequired {
		registrationOut, registrationErr := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show registration trunk-falepaco-reg").CombinedOutput()
		registrationPresent = registrationErr == nil && strings.Contains(string(registrationOut), "trunk-falepaco-reg") && !strings.Contains(string(registrationOut), "Unable to find object")
		if !registrationPresent {
			jsonOut(w, 502, map[string]any{"error": "asterisk_registration_readback_failed", "apply_stage": "health", "apply_error_class": "pjsip_registration_missing", "apply_error_summary": "registration object missing after reload", "secrets_redacted": true})
			return
		}
	}
	jsonOut(w, 200, map[string]any{"applied": true, "credential_file_loaded_fresh": true, "endpoint_active": report.EndpointActive, "auth_object_present": authPresent, "auth_username": req.Auth.Username, "auth_realm": req.RegistrationRealm, "outbound_auth_reference": "trunk-falepaco-auth", "transport": req.Transport, "request_uri_host": req.Host, "outbound_proxy": req.OutboundProxy, "caller_id": req.CallerID, "registration_required": req.RegistrationRequired, "registration_object_present": registrationPresent, "transport_object": transportName, "transport_object_present": true, "secrets_redacted": true})
}

var applyCredentialPattern = regexp.MustCompile(`(?i)(password|secret|authorization|proxy-authorization|digest response|nonce|cnonce|opaque)(\s*[=:]\s*)[^,;\s]+`)

func safeApplyError(err error, secret string) (stage, class, summary string) {
	if err == nil {
		return "", "", ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "res_resolver_unbound.so"):
		stage, class = "resolver", "pinned_resolver_unavailable"
	case strings.Contains(message, "invalid trunk configuration") || strings.Contains(message, "invalid SIP"):
		stage, class = "validation", "invalid_configuration"
	case strings.Contains(message, "DNS failure"):
		stage, class = "dns", "provider_dns_error"
	case strings.Contains(message, "Connection failed") || strings.Contains(message, "connection failure") || strings.Contains(message, "TLS connection"):
		stage, class = "reachability", "provider_unreachable"
	case strings.Contains(message, "PJSIP generation"):
		stage, class = "render", "pjsip_render_error"
	case strings.Contains(message, "stage") || strings.Contains(message, "Staging"):
		stage, class = "stage", "pjsip_stage_error"
	case strings.Contains(message, "reload") || strings.Contains(message, "Reload"):
		stage, class = "reload", "pjsip_configuration_error"
	case strings.Contains(message, "health check"):
		stage, class = "health", "asterisk_health_error"
	case strings.Contains(message, "transport"):
		stage, class = "transport", "pjsip_transport_error"
	case strings.Contains(message, "endpoint"):
		stage, class = "endpoint", "pjsip_endpoint_error"
	case strings.Contains(message, "registration"):
		stage, class = "registration_wait", "registration_not_ready"
	case strings.Contains(message, "commit"):
		stage, class = "commit", "pjsip_commit_error"
	default:
		stage, class = "apply", "sip_apply_error"
	}
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	message = applyCredentialPattern.ReplaceAllString(message, "$1$2[REDACTED]")
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return stage, class, message
}
