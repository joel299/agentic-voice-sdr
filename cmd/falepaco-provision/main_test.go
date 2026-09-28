package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

func TestProvisionRequestMapsRuntimeFields(t *testing.T) {
	credentials := runtimeCredentials{domain: canonicalDomain, username: "auth-user", extension: "100", password: "test-only-secret"}
	request := provisionRequest(credentials)
	canonical, err := request.ToCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Host != canonicalDomain || canonical.Registrar != canonicalDomain || canonical.AuthUsername != credentials.username || canonical.FromUser != credentials.extension || canonical.Secret != credentials.password || canonical.RegistrationRequired {
		t.Fatalf("runtime credential fields were not mapped into canonical trunk configuration")
	}
	generated, err := sip.GeneratePJSIPConfig(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if configValue(generated, "trunk-falepaco-auth", "username") != credentials.username || configValue(generated, "trunk-falepaco-auth", "password") != credentials.password {
		t.Fatal("generated auth did not use runtime credentials")
	}
	if canonical.HostNetworkAddress != "" || canonical.RegistrarNetworkAddress != "" || canonical.OutboundProxyNetworkAddress != "" {
		t.Fatal("resolved IP was supplied as persistent SIP destination")
	}
	if !strings.Contains(generated, "contact=sip:"+canonicalDomain+":5060") || !strings.Contains(generated, "outbound_auth=trunk-falepaco-auth") {
		t.Fatal("direct trunk lost canonical destination or digest auth")
	}
	if strings.Contains(generated, "[trunk-falepaco-reg]") {
		t.Fatal("registration object generated although registration is not required")
	}
}

func TestLoadRuntimeCredentialsFromRestrictedEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "falepaco.env")
	contents := "FALEPACO_SIP_DOMAIN=98034.falepaco.com.br\nFALEPACO_SIP_USERNAME=auth-user\nFALEPACO_SIP_EXTENSION=100\nFALEPACO_SIP_PASSWORD='test $special secret'\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	credentials, err := loadRuntimeCredentials(context.Background(), path)
	if err != nil {
		t.Fatalf("runtime env file did not load: %v", err)
	}
	if credentials.domain != canonicalDomain || credentials.username != "auth-user" || credentials.extension != "100" || credentials.password != "test $special secret" {
		t.Fatal("runtime env values were not loaded exactly")
	}
}

func TestLoadRuntimeCredentialsRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "falepaco.env")
	if err := os.WriteFile(path, []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntimeCredentials(context.Background(), path); err == nil {
		t.Fatal("runtime env file with broad permissions was accepted")
	}
}
