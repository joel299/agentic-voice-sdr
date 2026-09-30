package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
)

func canonicalTestRequest() httpapi.SIPConfigRequest {
	return httpapi.SIPConfigRequest{
		Host: "98034.falepaco.com.br", Port: 5060, Transport: "tcp", Registrar: "98034.falepaco.com.br",
		OutboundProxy: "98034.falepaco.com.br:5060", Auth: httpapi.SIPAuthRequest{Type: "userpass", Username: "100"},
		FromUser: "100", FromDomain: "98034.falepaco.com.br", CallerID: "551155200455", RegistrationRequired: true,
		RegistrationServerURI: "sip:98034.falepaco.com.br:5060", RegistrationClientURI: "sip:100@98034.falepaco.com.br:5060",
		RegistrationContactUser: "100", RegistrationRealm: "98034.falepaco.com.br", RegistrationRetryInterval: 60, RegistrationMaxRetries: 3,
	}
}

func activeProfileFixture() map[string]string {
	return map[string]string{
		"pjsip show registration trunk-falepaco-reg": "Status: Registered\n server_uri: sip:98034.falepaco.com.br:5060\n client_uri: sip:100@98034.falepaco.com.br:5060\n contact_user: 100\n outbound_proxy: sip:98034.falepaco.com.br:5060;transport=tcp;lr\n transport: transport-tcp\n",
		"pjsip show endpoint trunk-falepaco":         "Endpoint: trunk-falepaco\n aors: trunk-falepaco-aor\n outbound_auth: trunk-falepaco-auth\n callerid: 551155200455\n from_user: 100\n from_domain: 98034.falepaco.com.br\n",
		"pjsip show aor trunk-falepaco-aor":          "Aor: trunk-falepaco-aor\n contact: sip:98034.falepaco.com.br:5060;transport=tcp\n",
		"pjsip show auth trunk-falepaco-auth":        "Auth: trunk-falepaco-auth\n auth_type: userpass\n username: 100\n realm: 98034.falepaco.com.br\n",
		"pjsip show transport transport-tcp":         "Transport: transport-tcp\n protocol: tcp\n",
	}
}

func TestActivePJSIPProfileMustMatchSavedCanonicalConfig(t *testing.T) {
	fixture := activeProfileFixture()
	state, mismatch := activePJSIPProfileStatus(context.Background(), canonicalTestRequest(), func(_ context.Context, command string) (string, error) {
		return fixture[command], nil
	})
	if state != "Registered" || mismatch != "" {
		t.Fatalf("state=%q mismatch=%q", state, mismatch)
	}
	fixture["pjsip show aor trunk-falepaco-aor"] = "Aor: trunk-falepaco-aor\n contact: sip:96678.falepaco.com.br:5060;transport=tcp\n"
	state, mismatch = activePJSIPProfileStatus(context.Background(), canonicalTestRequest(), func(_ context.Context, command string) (string, error) {
		return fixture[command], nil
	})
	if state != "Registered" || mismatch != "contact_mismatch" {
		t.Fatalf("drifted active AOR accepted: state=%q mismatch=%q", state, mismatch)
	}
}

func TestCallProfileStopsBeforeOtherReadbacksUnlessRegistered(t *testing.T) {
	calls := 0
	state, mismatch := activePJSIPProfileStatus(context.Background(), canonicalTestRequest(), func(_ context.Context, command string) (string, error) {
		calls++
		if command != "pjsip show registration trunk-falepaco-reg" {
			t.Fatalf("unexpected readback before registration gate: %s", command)
		}
		return "Status: Rejected", nil
	})
	if state != "Rejected" || mismatch != "" || calls != 1 {
		t.Fatalf("state=%q mismatch=%q calls=%d", state, mismatch, calls)
	}
}

func TestCanonicalProfileRequiresAll98034TCPFields(t *testing.T) {
	req := canonicalTestRequest()
	env := map[string]string{"FALEPACO_SIP_DOMAIN": "98034.falepaco.com.br", "FALEPACO_SIP_OUTBOUND_HOST": "98034.falepaco.com.br"}
	if !canonicalFalePacoProfile(req, env) {
		t.Fatal("canonical 98034 profile rejected")
	}
	req.RegistrationClientURI = "sip:100@96678.falepaco.com.br:5060"
	if canonicalFalePacoProfile(req, env) {
		t.Fatal("legacy registration URI accepted as canonical")
	}
}

func TestCallDestinationIsExactAllowlist(t *testing.T) {
	if !callDestinationAllowed("+5567981340687") {
		t.Fatal("configured test destination rejected")
	}
	for _, destination := range []string{"+5567981340688", "+551155200455", "sip:+5567981340687@98034.falepaco.com.br"} {
		if callDestinationAllowed(destination) {
			t.Fatalf("unapproved destination accepted: %s", destination)
		}
	}
}

func TestActiveProfileMismatchDoesNotEchoSecretMaterial(t *testing.T) {
	fixture := activeProfileFixture()
	fixture["pjsip show auth trunk-falepaco-auth"] = "Auth: trunk-falepaco-auth\n auth_type: userpass\n username: 100\n realm: 96678.falepaco.com.br\n password: private-test-only\n"
	_, mismatch := activePJSIPProfileStatus(context.Background(), canonicalTestRequest(), func(_ context.Context, command string) (string, error) {
		return fixture[command], nil
	})
	if mismatch != "realm_mismatch" || strings.Contains(mismatch, "private-test-only") {
		t.Fatalf("unsafe or unexpected mismatch: %q", mismatch)
	}
}

func TestScalarOpenAPIUsesCanonicalProfileAndAllowedDestination(t *testing.T) {
	document, err := os.ReadFile("openapi.test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(document))
	if strings.Contains(text, "96678.falepaco.com.br") || strings.Contains(text, "+556****") || !strings.Contains(text, "+5567981340687") {
		t.Fatal("Scalar examples contain a legacy host or masked destination")
	}
	if !strings.Contains(text, "requires registration_status=registered") || !strings.Contains(text, "does not reapply or reload the trunk") {
		t.Fatal("Scalar does not explain the call registration gate and active-profile behavior")
	}
}
