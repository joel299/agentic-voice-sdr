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
	RegistrationRequired bool               `json:"registration_required"`
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
	Configured           bool   `json:"configured"`
	ProviderAddress      string `json:"provider_address"`
	RequestURIHost       string `json:"request_uri_host"`
	OutboundProxy        string `json:"outbound_proxy"`
	Username             string `json:"username"`
	Extension            string `json:"extension"`
	PasswordConfigured   bool   `json:"password_configured"`
	CallerID             string `json:"caller_id"`
	Transport            string `json:"transport"`
	Port                 int    `json:"port"`
	RegistrationRequired bool   `json:"registration_required"`
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
func atomicDotenvWrite(m map[string]string) error {
	dir := filepath.Dir(sipEnv)
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
	if e = os.Rename(name, sipEnv); e != nil {
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
		if _, e := fmt.Sscanf(m["FALEPACO_SIP_PORT"], "%d", &port); e != nil {
			return httpapi.SIPConfigRequest{}, e
		}
	}
	transport := m["FALEPACO_SIP_TRANSPORT"]
	if transport == "" {
		transport = "tcp"
	}
	reg := m["FALEPACO_SIP_REGISTRATION_REQUIRED"] == "true" || m["FALEPACO_SIP_REGISTRATION_ENABLED"] == "true"
	req := httpapi.SIPConfigRequest{Provider: "falepaco", Name: "falepaco", Host: host, Port: port, Transport: transport, Registrar: host, OutboundProxy: m["FALEPACO_SIP_OUTBOUND_PROXY"], FromDomain: host, FromUser: m["FALEPACO_SIP_EXTENSION"], CallerID: m["FALEPACO_SIP_CALLER_ID"], SendPAI: true, Auth: httpapi.SIPAuthRequest{Type: "userpass", Username: m["FALEPACO_SIP_USERNAME"], Secret: m["FALEPACO_SIP_PASSWORD"]}, RegistrationRequired: reg, Enabled: true}
	req.RegistrationServerURI = m["FALEPACO_SIP_REGISTRATION_SERVER_URI"]
	req.RegistrationClientURI = m["FALEPACO_SIP_REGISTRATION_CLIENT_URI"]
	req.RegistrationContactUser = m["FALEPACO_SIP_CONTACT_USER"]
	req.RegistrationRealm = m["FALEPACO_SIP_REALM"]
	req.RegistrationRetryInterval = 60
	req.RegistrationMaxRetries = 3
	return req, nil
}
func (s *server) falepacoGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	m, _ := dotenv(sipEnv)
	req, _ := savedFalepaco(m)
	jsonOut(w, 200, falepacoConfigResponse{Configured: m["FALEPACO_SIP_PASSWORD"] != "" && m["FALEPACO_SIP_USERNAME"] != "", ProviderAddress: m["FALEPACO_SIP_DOMAIN"], RequestURIHost: m["FALEPACO_SIP_OUTBOUND_HOST"], OutboundProxy: req.OutboundProxy, Username: req.Auth.Username, Extension: req.FromUser, PasswordConfigured: m["FALEPACO_SIP_PASSWORD"] != "", CallerID: req.CallerID, Transport: req.Transport, Port: req.Port, RegistrationRequired: req.RegistrationRequired})
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
	old, _ := dotenv(sipEnv)
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
	for _, h := range []string{in.ProviderAddress, in.RequestURIHost, strings.Split(in.OutboundProxy, ":")[0]} {
		if e := validSIPHost(h); e != nil {
			jsonOut(w, 400, map[string]string{"error": "invalid_sip_host"})
			return
		}
	}
	m := map[string]string{"FALEPACO_SIP_DOMAIN": in.ProviderAddress, "FALEPACO_SIP_OUTBOUND_HOST": in.RequestURIHost, "FALEPACO_SIP_OUTBOUND_PROXY": in.OutboundProxy, "FALEPACO_SIP_USERNAME": in.Username, "FALEPACO_SIP_EXTENSION": in.Extension, "FALEPACO_SIP_PASSWORD": password, "FALEPACO_SIP_CALLER_ID": in.CallerID, "FALEPACO_SIP_TRANSPORT": in.Transport, "FALEPACO_SIP_PORT": "5060", "FALEPACO_SIP_REGISTRATION_REQUIRED": fmt.Sprint(in.RegistrationRequired), "FALEPACO_SIP_REGISTRATION_ENABLED": fmt.Sprint(in.Registration.Enabled), "FALEPACO_SIP_REGISTRATION_SERVER_URI": in.Registration.ServerURI, "FALEPACO_SIP_REGISTRATION_CLIENT_URI": in.Registration.ClientURI, "FALEPACO_SIP_CONTACT_USER": in.Registration.ContactUser, "FALEPACO_SIP_REALM": in.Registration.Realm, "FALEPACO_SIP_RETRY_INTERVAL": fmt.Sprint(in.Registration.RetryIntervalSeconds), "FALEPACO_SIP_MAX_RETRIES": fmt.Sprint(in.Registration.MaxRetries)}
	if e := atomicDotenvWrite(m); e != nil {
		jsonOut(w, 500, map[string]string{"error": "credential_write_failed"})
		return
	}
	s.falepacoGet(w, r)
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
		jsonOut(w, 502, map[string]string{"error": "configuration_invalid"})
		return
	}
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, e := mgr.ApplyTrunk(ctx, cfg)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "sip_apply_failed"})
		return
	}
	out, _ := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show endpoint trunk-falepaco").CombinedOutput()
	if !strings.Contains(string(out), "trunk-falepaco") {
		jsonOut(w, 502, map[string]string{"error": "asterisk_readback_failed"})
		return
	}
	transportName := "transport-" + req.Transport
	transportOut, transportErr := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show transport "+transportName).CombinedOutput()
	if transportErr != nil || !strings.Contains(string(transportOut), transportName) {
		jsonOut(w, 502, map[string]string{"error": "asterisk_transport_readback_failed"})
		return
	}
	jsonOut(w, 200, map[string]any{"applied": true, "credential_file_loaded_fresh": true, "endpoint_active": report.EndpointActive, "outbound_auth_reference": "trunk-falepaco-auth", "auth_username": req.Auth.Username, "transport": req.Transport, "request_uri_host": req.Host, "outbound_proxy": req.OutboundProxy, "caller_id": req.CallerID, "registration_required": req.RegistrationRequired, "transport_object": transportName, "transport_object_present": true, "secrets_redacted": true})
}
