package sip

import "testing"

func TestVerifyDigestResponseUsesRuntimeSecret(t *testing.T) {
	username, password := "100", "runtime-secret"
	realm, nonce, method, uri := "96678.falepaco.com.br", "nonce", "INVITE", "sip:+5567981340687@96678.falepaco.com.br:5060;transport=tcp"
	qop, nc, cnonce := "auth", "00000001", "cnonce"
	ha1 := fmtMD5(username + ":" + realm + ":" + password)
	ha2 := fmtMD5(method + ":" + uri)
	actual := fmtMD5(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	if !VerifyDigestResponse(username, password, realm, nonce, method, uri, qop, nc, cnonce, actual) {
		t.Fatal("expected runtime secret to verify")
	}
	if VerifyDigestResponse(username, "wrong-secret", realm, nonce, method, uri, qop, nc, cnonce, actual) {
		t.Fatal("wrong secret must not verify")
	}
}
