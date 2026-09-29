package main

import "testing"

func TestSavedFalepacoUsesPersistedTransport(t *testing.T) {
	m := map[string]string{"FALEPACO_SIP_DOMAIN": "98034.falepaco.com.br", "FALEPACO_SIP_OUTBOUND_HOST": "96678.falepaco.com.br", "FALEPACO_SIP_OUTBOUND_PROXY": "98034.falepaco.com.br:5060", "FALEPACO_SIP_USERNAME": "100", "FALEPACO_SIP_EXTENSION": "100", "FALEPACO_SIP_PASSWORD": "secret", "FALEPACO_SIP_CALLER_ID": "551155200455", "FALEPACO_SIP_TRANSPORT": "udp", "FALEPACO_SIP_PORT": "5060"}
	req, err := savedFalepaco(m)
	if err != nil {
		t.Fatal(err)
	}
	if req.Transport != "udp" {
		t.Fatalf("transport=%s", req.Transport)
	}
	cfg, err := req.ToCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.Transport) != "udp" {
		t.Fatalf("canonical transport=%s", cfg.Transport)
	}
}
