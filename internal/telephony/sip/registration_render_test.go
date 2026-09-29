package sip

import (
	"strings"
	"testing"
)

func TestFalePacoPJSIPUsesPinnedAddressesWhenProvided(t *testing.T) {
	cfg := TrunkConfig{Provider: "falepaco", Name: "falepaco", Host: "98034.falepaco.com.br", HostNetworkAddress: "177.11.49.36", Registrar: "98034.falepaco.com.br", RegistrarNetworkAddress: "177.11.49.36", OutboundProxy: "98034.falepaco.com.br:5060", OutboundProxyNetworkAddress: "177.11.49.36:5060", Port: 5060, Transport: TransportTCP, AuthType: AuthUserPass, AuthUsername: "100", Secret: "secret", RegistrationRequired: true, RegistrationServerURI: "sip:98034.falepaco.com.br:5060", RegistrationClientURI: "sip:100@98034.falepaco.com.br:5060"}
	out, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"contact=sip:177.11.49.36:5060", "outbound_proxy=sip:177.11.49.36:5060", "; gru83-pin host=98034.falepaco.com.br address=177.11.49.36"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing pinned address %q in config:\n%s", want, out)
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
