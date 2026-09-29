package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

const (
	apiEnv  = "/root/agentic-voice-sdr/.runtime-secrets/sdr-api.env"
	sipEnv  = "/root/agentic-voice-sdr/.runtime-secrets/falepaco.env"
	allowed = "+5567981340687"
)

var callMu sync.Mutex

const sipCapture = "/var/log/asterisk/gru142-sip.log"

var headerRE = regexp.MustCompile(`(?i)(?:^|\n)(Proxy-Authenticate|Proxy-Authorization):[^\n]*`)
var fieldRE = regexp.MustCompile(`(?i)([a-z-]+)="?([^",\s]+)"?`)

type request struct {
	Destination string `json:"destination"`
}
type response struct {
	OK                                 bool   `json:"ok"`
	Destination                        string `json:"destination"`
	CredentialSource                   string `json:"credential_source"`
	CredentialFileLoadedFresh          bool   `json:"credential_file_loaded_fresh"`
	AuthUsername                       string `json:"auth_username"`
	Transport                          string `json:"transport"`
	RequestURI                         string `json:"request_uri"`
	OutboundProxyHost                  string `json:"outbound_proxy_host"`
	DigestChallengeReceived            bool   `json:"digest_challenge_received"`
	AuthenticatedInviteSent            bool   `json:"authenticated_invite_sent"`
	DigestResponseMatchesRuntimeSecret bool   `json:"digest_response_matches_runtime_secret"`
	SIPStatus                          int    `json:"sip_status"`
	SIPReason                          string `json:"sip_reason"`
	SecretsRedacted                    bool   `json:"secrets_redacted"`
	Error                              string `json:"error,omitempty"`
}

func dotenv(path string) (map[string]string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "'\"")
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}
func tokenOK(r *http.Request) bool {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return false
	}
	m, _ := dotenv(apiEnv)
	want := m["SDR_TEST_API_TOKEN"]
	got := strings.TrimPrefix(v, "Bearer ")
	return want != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func health(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"ok": true, "service": "sdr-test-api", "mode": "outbound-only"})
}
func (s *server) call(w http.ResponseWriter, r *http.Request) {
	if !tokenOK(r) {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in request
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Destination != allowed {
		jsonOut(w, 400, map[string]string{"error": "destination_not_allowed"})
		return
	}
	if !callMu.TryLock() {
		jsonOut(w, 409, map[string]string{"error": "call_already_active"})
		return
	}
	defer callMu.Unlock()
	c, e := dotenv(sipEnv)
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "credential_source_unavailable"})
		return
	}
	for _, k := range []string{"FALEPACO_SIP_DOMAIN", "FALEPACO_SIP_USERNAME", "FALEPACO_SIP_EXTENSION", "FALEPACO_SIP_PASSWORD"} {
		if c[k] == "" {
			jsonOut(w, 502, map[string]string{"error": "credential_source_incomplete"})
			return
		}
	}
	req := httpapi.SIPConfigRequest{Provider: "falepaco", Name: "falepaco", Host: "96678.falepaco.com.br", Port: 5060, Transport: "tcp", Registrar: "96678.falepaco.com.br", OutboundProxy: "98034.falepaco.com.br:5060", FromDomain: "96678.falepaco.com.br", FromUser: c["FALEPACO_SIP_EXTENSION"], CallerID: "551155200455", SendPAI: true, SendRPID: false, RegistrationRequired: false, Enabled: true, Auth: httpapi.SIPAuthRequest{Type: "userpass", Username: c["FALEPACO_SIP_USERNAME"], Secret: c["FALEPACO_SIP_PASSWORD"]}}
	cfg, e := req.ToCanonical()
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "sip_config_invalid"})
		return
	}
	mgr, e := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "asterisk_manager_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if _, e = mgr.ApplyTrunk(ctx, cfg); e != nil {
		jsonOut(w, 502, map[string]string{"error": "sip_apply_failed"})
		return
	}
	if out, e := exec.CommandContext(ctx, "asterisk", "-rx", "pjsip show endpoint trunk-falepaco").CombinedOutput(); e != nil || !strings.Contains(string(out), "trunk-falepaco") {
		jsonOut(w, 502, map[string]string{"error": "asterisk_readback_failed"})
		return
	}
	pcap := "/tmp/gru142-sip.pcap"
	_ = os.Remove(pcap)
	tcp := exec.CommandContext(ctx, "tcpdump", "-i", "any", "-s0", "-w", pcap, "tcp port 5060")
	if e = tcp.Start(); e != nil {
		jsonOut(w, 502, map[string]string{"error": "sip_capture_setup_failed"})
		return
	}
	if _, e = exec.CommandContext(ctx, "asterisk", "-rx", "channel originate PJSIP/"+allowed+"@trunk-falepaco application Wait 15").CombinedOutput(); e != nil {
		_ = tcp.Process.Kill()
		jsonOut(w, 502, map[string]string{"error": "originate_failed"})
		return
	}
	time.Sleep(18 * time.Second)
	_ = tcp.Process.Kill()
	_ = tcp.Wait()
	defer os.Remove(pcap)
	dump, e := exec.CommandContext(ctx, "tcpdump", "-nn", "-A", "-r", pcap).Output()
	if e != nil {
		jsonOut(w, 502, map[string]string{"error": "sip_capture_decode_failed"})
		return
	}
	challenge, auth, digestOK, status := parseDigest(string(dump), c["FALEPACO_SIP_PASSWORD"])
	if !challenge || !auth || !digestOK {
		jsonOut(w, 502, map[string]string{"error": "digest_proof_failed"})
		return
	}
	reason := ""
	if status == 403 {
		reason = "Forbidden"
	}
	if status == 200 {
		reason = "OK"
	}
	jsonOut(w, 200, response{OK: status >= 200 && status < 300, Destination: allowed, CredentialSource: "runtime_env_file", CredentialFileLoadedFresh: true, AuthUsername: c["FALEPACO_SIP_USERNAME"], Transport: "tcp", RequestURI: "sip:" + allowed + "@96678.falepaco.com.br:5060;transport=tcp", OutboundProxyHost: "98034.falepaco.com.br", DigestChallengeReceived: challenge, AuthenticatedInviteSent: auth, DigestResponseMatchesRuntimeSecret: digestOK, SIPStatus: status, SIPReason: reason, SecretsRedacted: true})

}

type server struct{}

func (s *server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><title>SDR Test API</title><link rel="stylesheet" href="/scalar.css"></head><body><div id="app"></div><script src="/scalar.js"></script><script>Scalar.createApiReference('#app',{url:'/openapi.yaml'})</script></body></html>`)
}

func parseDigest(wire, password string) (bool, bool, bool, int) {
	lines := headerRE.FindAllString(wire, -1)
	challenge, auth := "", ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(strings.ToLower(line), "proxy-authenticate:") && challenge == "" {
			challenge = line
		}
		if strings.HasPrefix(strings.ToLower(line), "proxy-authorization:") {
			auth = line
		}
	}
	status := 0
	statusRE := regexp.MustCompile(`SIP/2.0 (\d{3})`)
	for _, m := range statusRE.FindAllStringSubmatch(wire, -1) {
		if len(m) == 2 {
			fmt.Sscanf(m[1], "%d", &status)
		}
	}
	if challenge == "" || auth == "" {
		return challenge != "", auth != "", false, status
	}
	get := func(line, key string) string {
		for _, m := range fieldRE.FindAllStringSubmatch(line, -1) {
			if strings.EqualFold(m[1], key) {
				return strings.Trim(m[2], `"`)
			}
		}
		return ""
	}
	realm, nonce, algorithm, qop := get(challenge, "realm"), get(challenge, "nonce"), get(challenge, "algorithm"), get(challenge, "qop")
	username, uri, nc, cnonce, response := get(auth, "username"), get(auth, "uri"), get(auth, "nc"), get(auth, "cnonce"), get(auth, "response")
	if (algorithm != "" && !strings.EqualFold(algorithm, "MD5")) || realm == "" || nonce == "" || qop == "" || username == "" || uri == "" || nc == "" || cnonce == "" || response == "" {
		return true, true, false, status
	}
	return true, true, sip.VerifyDigestResponse(username, password, realm, nonce, "INVITE", uri, qop, nc, cnonce, response), status
}

func main() {
	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/docs", http.StatusFound) })
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, e := os.Stat(sipEnv); e != nil {
			jsonOut(w, 503, map[string]any{"ok": false})
		} else {
			health(w, r)
		}
	})
	mux.HandleFunc("/docs", s.docs)
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "/app/openapi.test.yaml") })
	mux.HandleFunc("/scalar.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "/app/scalar.js") })
	mux.HandleFunc("/scalar.css", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "/app/scalar.css") })
	mux.HandleFunc("/v1/test/falepaco/call", s.call)
	_ = http.ListenAndServe(":8081", mux)
}

var _ = errors.New
