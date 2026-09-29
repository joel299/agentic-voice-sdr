package main

import "testing"

func TestParseSIPWireCaptureExtractsSanitizedDigestEvidence(t *testing.T) {
	capture := `REGISTER sip:host SIP/2.0
SIP/2.0 401 Unauthorized
WWW-Authenticate: Digest realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", qop="auth", algorithm=MD5
REGISTER sip:host SIP/2.0
Authorization: Digest username="Mufasa", realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", uri="/dir/index.html", qop=auth, nc=00000001, cnonce="0a4f113b", response="59d17b90f0e821045ecceb843e5b38c4"
SIP/2.0 200 OK
Server: provider-test
`
	evidence := parseSIPWireCapture(capture, "Mufasa", "Circle Of Life")
	if !evidence.Initial || evidence.FirstResponse != "401" || !evidence.Challenge || evidence.ChallengeType != "401" || evidence.Realm != "testrealm@host.com" {
		t.Fatalf("challenge evidence incomplete: %+v", evidence)
	}
	if !evidence.Authenticated || evidence.AuthUsername != "Mufasa" || evidence.AuthRealm != "testrealm@host.com" || evidence.AuthURI != "/dir/index.html" || evidence.DigestMatches == nil || !*evidence.DigestMatches {
		t.Fatalf("digest evidence incorrect: %+v", evidence)
	}
	if evidence.FinalResponse != "200" || evidence.FinalReason != "OK" || evidence.Server != "provider-test" {
		t.Fatalf("final response evidence incomplete: %+v", evidence)
	}
}

func TestParseSIPWireCaptureDoesNotClaimUnseenTraffic(t *testing.T) {
	evidence := parseSIPWireCapture("tcpdump: listening on any", "100", "secret")
	if evidence.Initial || evidence.Challenge || evidence.Authenticated || evidence.FinalResponse != "" {
		t.Fatalf("capture without SIP packets must not imply REGISTER evidence: %+v", evidence)
	}
}
