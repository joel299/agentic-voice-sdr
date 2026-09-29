package sip

import (
	"strings"
	"testing"
)

func TestRegistrationRenderIncludesExplicitRuntimeFields(t *testing.T) {
	c := TrunkConfig{Provider: "falepaco", Name: "falepaco", Host: "96678.falepaco.com.br", Port: 5060, Transport: TransportTCP, AuthType: AuthUserPass, AuthUsername: "100", Secret: "secret", FromUser: "100", RegistrationRequired: true, RegistrationServerURI: "sip:96678.falepaco.com.br:5060", RegistrationClientURI: "sip:100@96678.falepaco.com.br:5060", RegistrationContactUser: "100", RegistrationRealm: "96678.falepaco.com.br", RegistrationRetryInterval: 60, RegistrationMaxRetries: 3, OutboundProxy: "98034.falepaco.com.br:5060"}
	out, e := GeneratePJSIPConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"[trunk-falepaco-reg]", "transport=transport-tcp", "server_uri=sip:96678.falepaco.com.br:5060", "client_uri=sip:100@96678.falepaco.com.br:5060", "contact_user=100", "realm=96678.falepaco.com.br", "retry_interval=60", "max_retries=3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
