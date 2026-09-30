package sip

import "crypto/md5"

// VerifyDigestResponse validates a SIP Digest response without exposing any inputs.
// It supports the MD5 qop=auth challenge used by the Fale Paco trunk.
func VerifyDigestResponse(username, password, realm, nonce, method, uri, qop, nc, cnonce, actual string) bool {
	h := func(value string) string { return fmtMD5(value) }
	ha1 := h(username + ":" + realm + ":" + password)
	ha2 := h(method + ":" + uri)
	expected := h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	return subtleConstantTimeCompare(expected, actual)
}

func fmtMD5(value string) string {
	sum := md5.Sum([]byte(value))
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range sum {
		out[i*2], out[i*2+1] = hex[b>>4], hex[b&15]
	}
	return string(out)
}

func subtleConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
