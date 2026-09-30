package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

const (
	apiEnv             = "/root/agentic-voice-sdr/.runtime-secrets/sdr-api.env"
	sipEnv             = "/root/agentic-voice-sdr/.runtime-secrets/falepaco.env"
	allowedDestination = "+5567981340687"
	issuer             = "sdr-test-api"
	audience           = "sdr-test-api"
)

var callMu sync.Mutex
var loginMu sync.Mutex
var loginAttempts = map[string]rateWindow{}

type rateWindow struct {
	started time.Time
	count   int
}
type server struct{}
type callRequest struct {
	Destination string `json:"destination"`
}
type tokenClaims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	JTI string `json:"jti"`
}
type callResponse struct {
	OK                                 bool    `json:"ok"`
	Destination                        string  `json:"destination"`
	RegistrationStatus                 string  `json:"registration_status"`
	SelectedTransport                  string  `json:"selected_transport"`
	RequestURIHost                     string  `json:"request_uri_host"`
	RequestURI                         string  `json:"request_uri"`
	OutboundProxyHost                  string  `json:"outbound_proxy_host"`
	AuthUsername                       string  `json:"auth_username"`
	OriginateAccepted                  bool    `json:"originate_accepted"`
	InviteSent                         bool    `json:"invite_sent"`
	DigestChallengeReceived            bool    `json:"digest_challenge_received"`
	AuthenticatedInviteSent            bool    `json:"authenticated_invite_sent"`
	DigestResponseMatchesRuntimeSecret bool    `json:"digest_response_matches_runtime_secret"`
	SIPProgress                        []int   `json:"sip_progress"`
	SIPFinalStatus                     int     `json:"sip_final_status"`
	SIPFinalReason                     string  `json:"sip_final_reason"`
	CallEstablished                    bool    `json:"call_established"`
	SDPNegotiated                      bool    `json:"sdp_negotiated"`
	Codec                              string  `json:"codec"`
	RTPActivityPresent                 bool    `json:"rtp_activity_present"`
	Q850Cause                          int     `json:"q850_cause"`
	Q850Reason                         string  `json:"q850_reason"`
	DurationSeconds                    float64 `json:"duration_seconds"`
	SecretsRedacted                    bool    `json:"secrets_redacted"`
	NetworkProtocolObserved            string  `json:"network_protocol_observed"`
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
		if ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), "'\"")
		}
	}
	return out, nil
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func b64(v []byte) string            { return base64.RawURLEncoding.EncodeToString(v) }
func unb64(v string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(v) }
func signToken(c tokenClaims, secret string) (string, error) {
	h := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	p, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	ps := b64(p)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(h + "." + ps))
	return h + "." + ps + "." + b64(mac.Sum(nil)), nil
}
func verifyToken(raw string, m map[string]string) (tokenClaims, bool) {
	var zero tokenClaims
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || m["SDR_TEST_JWT_SECRET"] == "" {
		return zero, false
	}
	mac := hmac.New(sha256.New, []byte(m["SDR_TEST_JWT_SECRET"]))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	got, e := unb64(parts[2])
	if e != nil || subtle.ConstantTimeCompare(got, mac.Sum(nil)) != 1 {
		return zero, false
	}
	payload, e := unb64(parts[1])
	if e != nil {
		return zero, false
	}
	var c tokenClaims
	if json.Unmarshal(payload, &c) != nil {
		return zero, false
	}
	now := time.Now().Unix()
	if c.Sub == "" || c.Iat <= 0 || c.Exp <= now || c.Iat > now+60 || c.Iss != issuer || c.Aud != audience || c.JTI == "" {
		return zero, false
	}
	return c, true
}
func bearer(r *http.Request) (tokenClaims, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return tokenClaims{}, false
	}
	m, e := dotenv(apiEnv)
	if e != nil {
		return tokenClaims{}, false
	}
	return verifyToken(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), m)
}
func loginAllowed(ip string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	x := loginAttempts[ip]
	if x.started.IsZero() || now.Sub(x.started) >= time.Minute {
		x = rateWindow{started: now}
	}
	x.count++
	loginAttempts[ip] = x
	return x.count <= 5
}
func remoteIP(r *http.Request) string {
	h, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return h
	}
	return r.RemoteAddr
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !loginAllowed(remoteIP(r)) {
		w.Header().Set("Retry-After", "60")
		jsonOut(w, 429, map[string]string{"error": "rate_limit_exceeded"})
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Username) == "" || in.Password == "" {
		jsonOut(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	m, e := dotenv(apiEnv)
	if e != nil || m["SDR_TEST_USERNAME"] == "" || m["SDR_TEST_PASSWORD_HASH"] == "" {
		jsonOut(w, 503, map[string]string{"error": "auth_unavailable"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Username), []byte(m["SDR_TEST_USERNAME"])) != 1 || bcrypt.CompareHashAndPassword([]byte(m["SDR_TEST_PASSWORD_HASH"]), []byte(in.Password)) != nil {
		jsonOut(w, 401, map[string]string{"error": "invalid_credentials"})
		return
	}
	now := time.Now()
	ttl := 15 * time.Minute
	if d, e := time.ParseDuration(m["SDR_TEST_JWT_TTL"]); e == nil && d > 0 && d <= time.Hour {
		ttl = d
	}
	tok, e := signToken(tokenClaims{Sub: m["SDR_TEST_USERNAME"], Iat: now.Unix(), Exp: now.Add(ttl).Unix(), Iss: issuer, Aud: audience, JTI: fmt.Sprintf("%d-%d", now.UnixNano(), os.Getpid())}, m["SDR_TEST_JWT_SECRET"])
	if e != nil {
		jsonOut(w, 500, map[string]string{"error": "token_issue_failed"})
		return
	}
	jsonOut(w, 200, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": int(ttl.Seconds())})
}
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	c, ok := bearer(r)
	if !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	jsonOut(w, 200, map[string]any{"authenticated": true, "username": c.Sub})
}
func health(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"ok": true, "service": "sdr-test-api", "mode": "outbound-only"})
}

var headerRE = regexp.MustCompile(`(?im)^(WWW-Authenticate|Proxy-Authenticate|Authorization|Proxy-Authorization):[^\r\n]*`)
var fieldRE = regexp.MustCompile(`(?i)([a-z-]+)="?([^",\s]+)"?`)

func parseDigest(wire, password string) (bool, bool, bool, int) {
	lines := headerRE.FindAllString(wire, -1)
	wwwChallenge, proxyChallenge, authorization, proxyAuthorization := "", "", "", ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "www-authenticate:") && wwwChallenge == "" {
			wwwChallenge = line
		}
		if strings.HasPrefix(lower, "proxy-authenticate:") && proxyChallenge == "" {
			proxyChallenge = line
		}
		if strings.HasPrefix(lower, "authorization:") && authorization == "" {
			authorization = line
		}
		if strings.HasPrefix(lower, "proxy-authorization:") && proxyAuthorization == "" {
			proxyAuthorization = line
		}
	}
	challenge, auth := wwwChallenge, authorization
	if proxyChallenge != "" && proxyAuthorization != "" {
		challenge, auth = proxyChallenge, proxyAuthorization
	} else if wwwChallenge == "" && proxyChallenge != "" {
		challenge = proxyChallenge
	} else if authorization == "" && proxyAuthorization != "" {
		auth = proxyAuthorization
	}
	hasChallenge := wwwChallenge != "" || proxyChallenge != ""
	hasAuth := authorization != "" || proxyAuthorization != ""
	status := 0
	for _, m := range regexp.MustCompile(`SIP/2.0 (\d{3})`).FindAllStringSubmatch(wire, -1) {
		if len(m) == 2 {
			_, _ = fmt.Sscanf(m[1], "%d", &status)
		}
	}
	if challenge == "" || auth == "" {
		return hasChallenge, hasAuth, false, status
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
	challengeRealm := get(challenge, "realm")
	pairedHeaders := (strings.HasPrefix(strings.ToLower(challenge), "www-authenticate:") && strings.HasPrefix(strings.ToLower(auth), "authorization:")) ||
		(strings.HasPrefix(strings.ToLower(challenge), "proxy-authenticate:") && strings.HasPrefix(strings.ToLower(auth), "proxy-authorization:"))
	return hasChallenge, hasAuth, pairedHeaders && strings.EqualFold(challengeRealm, realm) && sip.VerifyDigestResponse(username, password, realm, nonce, "INVITE", uri, qop, nc, cnonce, response), status
}
func classifyCaptureError(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "permission denied"):
		return "permission_denied"
	case strings.Contains(t, "truncated") || strings.Contains(t, "unexpected end"):
		return "truncated_pcap"
	case strings.Contains(t, "invalid") || strings.Contains(t, "not a pcap"):
		return "invalid_pcap"
	case strings.Contains(t, "timed out"):
		return "context_timeout"
	default:
		return "unknown"
	}
}
func (s *server) call(w http.ResponseWriter, r *http.Request) {
	if _, ok := bearer(r); !ok {
		jsonOut(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in callRequest
	if json.NewDecoder(r.Body).Decode(&in) != nil || !callDestinationAllowed(in.Destination) {
		jsonOut(w, 400, map[string]string{"error": "destination_not_allowed"})
		return
	}
	if !callMu.TryLock() {
		jsonOut(w, 409, map[string]string{"error": "call_already_active"})
		return
	}
	defer callMu.Unlock()
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
		jsonOut(w, 502, map[string]string{"error": "sip_config_invalid"})
		return
	}
	if cfg.RegistrationRequired {
		state, mismatch := activePJSIPProfileStatus(r.Context(), req, runAsteriskCommand)
		if !callRegistrationReady(state) {
			jsonOut(w, 409, map[string]any{"error": "provider_not_registered", "registration_status": state, "secrets_redacted": true})
			return
		}
		if mismatch != "" {
			jsonOut(w, 409, map[string]any{"error": "active_profile_mismatch", "active_profile_mismatch": mismatch, "registration_status": state, "secrets_redacted": true})
			return
		}
	}
	if !canonicalFalePacoProfile(req, m) {
		jsonOut(w, 409, map[string]any{"error": "configuration_not_canonical", "secrets_redacted": true})
		return
	}
	requestURI, e := sip.BuildOutboundURI(cfg, allowedDestination)
	if e != nil {
		jsonOut(w, 502, map[string]any{"error": "sip_config_invalid", "secrets_redacted": true})
		return
	}
	dnsState, providerIPs, allowed := resolveFalePacoHost(req.Host, 4*time.Second)
	if dnsState != "resolved" || !allowed || len(providerIPs) == 0 {
		jsonOut(w, 502, map[string]any{"error": "provider_dns_not_allowed", "secrets_redacted": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	protocol := strings.ToLower(req.Transport)
	if protocol != "tcp" && protocol != "udp" {
		jsonOut(w, 400, map[string]string{"error": "transport_not_supported"})
		return
	}
	capture, pcap, e := startSIPCallPCAPCapture(providerIPs)
	if e != nil {
		jsonOut(w, 502, map[string]any{"error": "sip_capture_setup_failed", "secrets_redacted": true})
		return
	}
	started := time.Now().UTC()
	originateOut, e := exec.CommandContext(ctx, "asterisk", "-rx", "channel originate PJSIP/"+allowedDestination+"@trunk-falepaco application Wait 15").CombinedOutput()
	originateAccepted := e == nil && !strings.Contains(strings.ToLower(string(originateOut)), "failed")
	if !originateAccepted {
		_ = stopSIPPCAPCapture(capture)
		_ = os.Remove(pcap)
		jsonOut(w, 502, map[string]any{"error": "originate_failed", "registration_status": "Registered", "originate_accepted": false, "secrets_redacted": true})
		return
	}
	if waitErr := waitForCallCapture(ctx, time.Until(started.Add(18*time.Second))); waitErr != nil {
		_ = stopSIPPCAPCapture(capture)
		_ = os.Remove(pcap)
		jsonOut(w, 502, map[string]any{"error": "call_capture_interrupted", "originate_accepted": true, "secrets_redacted": true})
		return
	}
	if stopErr := stopSIPPCAPCapture(capture); stopErr != nil {
		_ = os.Remove(pcap)
		jsonOut(w, 502, map[string]any{"error": "sip_capture_stop_failed", "originate_accepted": true, "secrets_redacted": true})
		return
	}
	evidence, messages, parseErr := readSIPCallPCAPAndRemove(pcap, started, req.Auth.Username, req.Auth.Secret)
	if parseErr != nil {
		jsonOut(w, 502, map[string]any{"error": "sip_capture_decode_failed", "originate_accepted": true, "secrets_redacted": true})
		return
	}
	callEvidence := summarizeSIPCall(evidence, messages, req.Auth.Username, req.Auth.Secret, req.RegistrationRealm)
	jsonOut(w, 200, callResponse{
		OK:          callEvidence.Established && callEvidence.SDPNegotiated && evidence.RTPActivityPresent,
		Destination: allowedDestination, RegistrationStatus: "Registered", SelectedTransport: protocol,
		RequestURIHost: req.Host, RequestURI: requestURI, OutboundProxyHost: req.OutboundProxy,
		AuthUsername: req.Auth.Username, OriginateAccepted: true, InviteSent: callEvidence.InviteSent,
		DigestChallengeReceived: callEvidence.Challenge, AuthenticatedInviteSent: callEvidence.Authenticated,
		DigestResponseMatchesRuntimeSecret: callEvidence.DigestMatches,
		SIPProgress:                        callEvidence.Progress, SIPFinalStatus: callEvidence.FinalStatus,
		SIPFinalReason: callEvidence.FinalReason, CallEstablished: callEvidence.Established,
		SDPNegotiated: callEvidence.SDPNegotiated, Codec: callEvidence.Codec,
		RTPActivityPresent: evidence.RTPActivityPresent, Q850Cause: callEvidence.Q850Cause,
		Q850Reason: callEvidence.Q850Reason, DurationSeconds: callEvidence.DurationSeconds,
		SecretsRedacted: true, NetworkProtocolObserved: observedProtocolFromCapture(evidence.CaptureMode),
	})
}

func observedProtocolFromCapture(mode string) string {
	if strings.Contains(mode, "tcp") {
		return "TCP"
	}
	if strings.Contains(mode, "udp") {
		return "UDP"
	}
	return ""
}
func (s *server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><title>SDR Test API</title><link rel="stylesheet" href="/scalar.css"></head><body><div id="app"></div><script src="/scalar.js"></script><script>Scalar.createApiReference('#app',{url:'/openapi.yaml'})</script></body></html>`)
}
func runCLI() {
	if len(os.Args) < 3 || os.Args[1] != "set-credentials" {
		fmt.Fprintln(os.Stderr, "usage: sdr-test-api set-credentials --username <username>")
		os.Exit(2)
	}
	u := ""
	if strings.HasPrefix(os.Args[2], "--username=") {
		u = strings.TrimPrefix(os.Args[2], "--username=")
	} else if os.Args[2] == "--username" && len(os.Args) >= 4 {
		u = os.Args[3]
	}
	if u == "" {
		fmt.Fprintln(os.Stderr, "username required")
		os.Exit(2)
	}
	fmt.Fprint(os.Stderr, "Password: ")
	p, _ := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "Confirm password: ")
	q, _ := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if string(p) != string(q) || len(p) < 12 {
		fmt.Fprintln(os.Stderr, "password confirmation failed")
		os.Exit(2)
	}
	hash, e := bcrypt.GenerateFromPassword(p, bcrypt.DefaultCost)
	if e != nil {
		fmt.Fprintln(os.Stderr, "hash failed")
		os.Exit(1)
	}
	m, _ := dotenv(apiEnv)
	secret := m["SDR_TEST_JWT_SECRET"]
	if secret == "" {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			for i := range b {
				b[i] = byte(time.Now().UnixNano() >> uint(i%8))
			}
		}
		secret = b64(b)
	}
	ttl := m["SDR_TEST_JWT_TTL"]
	if ttl == "" {
		ttl = "15m"
	}
	f, _ := os.OpenFile(apiEnv, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	defer f.Close()
	_, _ = fmt.Fprintf(f, "SDR_TEST_USERNAME=%s\nSDR_TEST_PASSWORD_HASH=%s\nSDR_TEST_JWT_SECRET=%s\nSDR_TEST_JWT_TTL=%s\nSDR_TEST_ALLOWED_DESTINATION=%s\n", u, hash, secret, ttl, allowedDestination)
}
func main() {
	if len(os.Args) > 1 {
		runCLI()
		return
	}
	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/docs", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
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
	mux.HandleFunc("/v1/auth/login", s.login)
	mux.HandleFunc("/v1/auth/me", s.me)
	mux.HandleFunc("/v1/falepaco/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.falepacoGet(w, r)
		} else if r.Method == http.MethodPut {
			s.falepacoPut(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/falepaco/config/apply", s.falepacoApply)
	mux.HandleFunc("/v1/falepaco/network/preflight", s.networkPreflight)
	mux.HandleFunc("/v1/falepaco/registration", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.registrationGet(w, r)
		} else if r.Method == http.MethodDelete {
			s.registrationDelete(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/falepaco/registration/test", s.registrationTest)
	mux.HandleFunc("/v1/test/falepaco/call", s.call)
	_ = http.ListenAndServe(":8081", mux)
}

var _ = errors.New
