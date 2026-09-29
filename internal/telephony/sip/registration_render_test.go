package sip

import (
	"strings"
	"testing"
)

func TestFalePacoPJSIPPreservesHostnameWithPinnedAddresses(t *testing.T) {
	cfg := TrunkConfig{Provider: "falepaco", Name: "falepaco", Host: "98034.falepaco.com.br", HostNetworkAddress: "177.11.49.36", Registrar: "98034.falepaco.com.br", RegistrarNetworkAddress: "177.11.49.36", OutboundProxy: "98034.falepaco.com.br:5060", OutboundProxyNetworkAddress: "177.11.49.36:5060", Port: 5060, Transport: TransportTCP, AuthType: AuthUserPass, AuthUsername: "100", Secret: "secret", FromUser: "100", FromDomain: "98034.falepaco.com.br", RegistrationRequired: true, RegistrationServerURI: "sip:98034.falepaco.com.br:5060", RegistrationClientURI: "sip:100@98034.falepaco.com.br:5060", RegistrationContactUser: "100", RegistrationRealm: "98034.falepaco.com.br"}
	out, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`contact=sip:98034.falepaco.com.br:5060\;transport=tcp`, `outbound_proxy=sip:98034.falepaco.com.br:5060\;transport=tcp\;lr`, "server_uri=sip:98034.falepaco.com.br:5060", "client_uri=sip:100@98034.falepaco.com.br:5060", "from_domain=98034.falepaco.com.br", "; gru83-pin host=98034.falepaco.com.br address=177.11.49.36"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in config:\\n%s", want, out)
		}
	}
	for _, activeLine := range strings.Split(out, "\n") {
		if (strings.HasPrefix(activeLine, "contact=") || strings.HasPrefix(activeLine, "outbound_proxy=") || strings.HasPrefix(activeLine, "server_uri=") || strings.HasPrefix(activeLine, "client_uri=") || strings.HasPrefix(activeLine, "from_domain=")) && strings.Contains(activeLine, "177.11.49.36") {
			t.Fatalf("resolved IP leaked into active SIP URI: %s", activeLine)
		}
	}
}

func TestRegistrationRenderIncludesExplicitRuntimeFields(t *testing.T) {
	c := TrunkConfig{Provider: "falepaco", Name: "falepaco", Host: "98034.falepaco.com.br", Port: 5060, Transport: TransportTCP, AuthType: AuthUserPass, AuthUsername: "100", Secret: "secret", FromUser: "100", RegistrationRequired: true, RegistrationServerURI: "sip:98034.falepaco.com.br:5060", RegistrationClientURI: "sip:100@98034.falepaco.com.br:5060", RegistrationContactUser: "100", RegistrationRealm: "98034.falepaco.com.br", RegistrationRetryInterval: 60, RegistrationMaxRetries: 3, OutboundProxy: "98034.falepaco.com.br:5060"}
	out, e := GeneratePJSIPConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"[trunk-falepaco-reg]", "transport=transport-tcp", "server_uri=sip:98034.falepaco.com.br:5060", "client_uri=sip:100@98034.falepaco.com.br:5060", "contact_user=100", "retry_interval=60", "max_retries=3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	authObject := out[strings.Index(out, "[trunk-falepaco-auth]"):strings.Index(out, "[trunk-falepaco-aor]")]
	registrationObject := out[strings.Index(out, "[trunk-falepaco-reg]"):]
	if !strings.Contains(authObject, "realm=98034.falepaco.com.br") {
		t.Fatal("realm must be emitted on auth object")
	}
	if strings.Contains(registrationObject, "realm=") {
		t.Fatal("realm must not be emitted on registration object")
	}
}
