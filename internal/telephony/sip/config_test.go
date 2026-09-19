package sip

import "testing"

func TestConfigValidationAndSafeView(t *testing.T) {
	cfg := Config{
		Provider: "generic-sip", Name: "primary", Host: "sip.example.test", Port: 5061,
		Transport: "tls", Registrar: "sip.example.test", Auth: Auth{Type: "password", Username: "alice", Secret: "secret"},
		FromUser: "alice", FromDomain: "example.test", CallerID: "+5511999999999", Codecs: []string{"opus"}, Enabled: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if safe := cfg.SafeView(); safe.Auth.Username != "alice" {
		t.Fatalf("safe view lost username: %+v", safe)
	}
}

func TestConfigValidationRejectsInvalidPayloads(t *testing.T) {
	cases := []Config{
		{Provider: "", Name: "x", Host: "sip.example.test", Port: 5060, Transport: "udp"},
		{Provider: "p", Name: "x", Host: "sip.example.test", Port: 0, Transport: "udp"},
		{Provider: "p", Name: "x", Host: "sip.example.test", Port: 5060, Transport: "smtp"},
		{Provider: "p", Name: "x", Host: "sip.example.test", Port: 5060, Transport: "udp", Auth: Auth{Type: "password", Username: "u"}},
	}
	for _, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected rejection: %+v", cfg)
		}
	}
}
