package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

func TestCallParserClassifiesChallengeAuthorizationProgressSDPRTPAndQ850(t *testing.T) {
	const secret = "call-test-secret"
	const realm = "98034.falepaco.com.br"
	const requestURI = "sip:+5567981340687@98034.falepaco.com.br:5060;transport=tcp"
	base := time.Now().UTC()
	messages := []sipPCAPMessage{
		{method: "INVITE", requestURI: requestURI, headers: map[string]string{"call-id": "call-test", "cseq": "1 INVITE", "from": "<sip:100@98034.falepaco.com.br>", "to": "<sip:+5567981340687@98034.falepaco.com.br>"}, seen: base},
		{startLine: "SIP/2.0 401 Unauthorized", headers: map[string]string{"call-id": "call-test", "cseq": "1 INVITE", "www-authenticate": `Digest realm="98034.falepaco.com.br", nonce="test-nonce", algorithm=MD5, qop="auth"`}, seen: base.Add(100 * time.Millisecond)},
		{method: "INVITE", requestURI: requestURI, headers: map[string]string{"call-id": "call-test", "cseq": "2 INVITE", "authorization": callTestAuthorization("100", realm, secret, requestURI, "test-nonce")}, seen: base.Add(200 * time.Millisecond)},
		{startLine: "SIP/2.0 183 Session Progress", headers: map[string]string{"call-id": "call-test", "cseq": "2 INVITE"}, body: []byte("v=0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\n"), seen: base.Add(500 * time.Millisecond)},
		{startLine: "SIP/2.0 200 OK", headers: map[string]string{"call-id": "call-test", "cseq": "2 INVITE"}, body: []byte("v=0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\n"), seen: base.Add(time.Second)},
		{method: "ACK", headers: map[string]string{"call-id": "call-test", "cseq": "2 ACK"}, seen: base.Add(1100 * time.Millisecond)},
		{method: "BYE", headers: map[string]string{"call-id": "call-test", "cseq": "3 BYE", "reason": `Q.850;cause=16;text="NORMAL_CLEARING"`}, seen: base.Add(20 * time.Second)},
		{startLine: "SIP/2.0 200 OK", headers: map[string]string{"call-id": "call-test", "cseq": "3 BYE", "reason": `Q.850;cause=16;text="NORMAL_CLEARING"`}, seen: base.Add(20*time.Second + 100*time.Millisecond)},
	}
	evidence := summarizeSIPCall(sipWireEvidence{RTPActivityPresent: true}, messages, "100", secret, realm)
	if !evidence.InviteSent || !evidence.Challenge || !evidence.Authenticated || !evidence.DigestMatches {
		t.Fatalf("INVITE/Digest evidence incomplete: %+v", evidence)
	}
	if len(evidence.Progress) != 1 || evidence.Progress[0] != 183 || evidence.FinalStatus != 200 || evidence.FinalReason != "OK" || !evidence.Established {
		t.Fatalf("SIP call sequence incomplete: %+v", evidence)
	}
	if !evidence.SDPNegotiated || evidence.Codec != "PCMU/8000" || evidence.Q850Cause != 16 || evidence.Q850Reason != "NORMAL_CLEARING" || evidence.DurationSeconds < 19 {
		t.Fatalf("SDP/media/clear evidence incorrect: %+v", evidence)
	}
	response, err := json.Marshal(callResponse{Destination: allowedDestination, RequestURI: requestURI, AuthUsername: "100", SIPFinalReason: evidence.FinalReason, Codec: evidence.Codec, Q850Reason: evidence.Q850Reason, SecretsRedacted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{secret, "test-nonce", "clientnonce", "response=", "Authorization:"} {
		if strings.Contains(string(response), private) {
			t.Fatalf("call response leaked private SIP material (%q): %s", private, response)
		}
	}
}

func TestDigestParserSeparatesChallengeAndAuthorizationHeaders(t *testing.T) {
	const secret = "call-test-secret"
	const realm = "98034.falepaco.com.br"
	const uri = "sip:+5567981340687@98034.falepaco.com.br:5060;transport=tcp"
	auth := callTestAuthorization("100", realm, secret, uri, "test-nonce")
	good := "SIP/2.0 401 Unauthorized\r\nWWW-Authenticate: Digest realm=\"" + realm + "\", nonce=\"test-nonce\", algorithm=MD5, qop=\"auth\"\r\n\r\nINVITE sip:test SIP/2.0\r\nAuthorization: " + auth + "\r\n\r\nSIP/2.0 200 OK\r\n"
	challenge, authenticated, matches, status := parseDigest(good, secret)
	if !challenge || !authenticated || !matches || status != 200 {
		t.Fatalf("WWW challenge/Authorization pair not parsed: challenge=%v auth=%v matches=%v status=%d", challenge, authenticated, matches, status)
	}
	challengeOnly := "SIP/2.0 401 Unauthorized\r\nWWW-Authenticate: Digest realm=\"" + realm + "\", nonce=\"test-nonce\"\r\n"
	challenge, authenticated, matches, _ = parseDigest(challengeOnly, secret)
	if !challenge || authenticated || matches {
		t.Fatalf("challenge header was confused with auth header: challenge=%v auth=%v matches=%v", challenge, authenticated, matches)
	}
	mismatchedPair := "SIP/2.0 407 Proxy Authentication Required\r\nWWW-Authenticate: Digest realm=\"" + realm + "\", nonce=\"test-nonce\"\r\nProxy-Authorization: " + auth + "\r\n"
	challenge, authenticated, matches, _ = parseDigest(mismatchedPair, secret)
	if !challenge || !authenticated || matches {
		t.Fatalf("mismatched challenge/auth header types were accepted: challenge=%v auth=%v matches=%v", challenge, authenticated, matches)
	}
}

func TestCallParserRequiresDigestRealmToMatchCanonicalRuntimeRealm(t *testing.T) {
	const secret = "call-test-secret"
	const uri = "sip:+5567981340687@98034.falepaco.com.br:5060;transport=tcp"
	base := time.Now().UTC()
	messages := []sipPCAPMessage{
		{method: "INVITE", requestURI: uri, headers: map[string]string{"call-id": "mismatch", "cseq": "1 INVITE"}, seen: base},
		{startLine: "SIP/2.0 401 Unauthorized", headers: map[string]string{"call-id": "mismatch", "cseq": "1 INVITE", "www-authenticate": `Digest realm="96678.falepaco.com.br", nonce="n", qop="auth"`}, seen: base.Add(time.Millisecond)},
		{method: "INVITE", requestURI: uri, headers: map[string]string{"call-id": "mismatch", "cseq": "2 INVITE", "authorization": callTestAuthorization("100", "96678.falepaco.com.br", secret, uri, "n")}, seen: base.Add(2 * time.Millisecond)},
	}
	got := summarizeSIPCall(sipWireEvidence{}, messages, "100", secret, "98034.falepaco.com.br")
	if !got.Authenticated || got.DigestMatches {
		t.Fatalf("legacy realm was accepted: %+v", got)
	}
}

func TestParseNegotiatedSDPAndQ850Reason(t *testing.T) {
	if ok, codec := parseNegotiatedSDP("v=0\r\nm=audio 9000 RTP/AVP 0\r\n"); !ok || codec != "PCMU/8000" {
		t.Fatalf("static PCMU not parsed: ok=%v codec=%q", ok, codec)
	}
	if ok, codec := parseNegotiatedSDP("v=0\r\nm=audio 9000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"); !ok || codec != "PCMA/8000" {
		t.Fatalf("PCMA not parsed: ok=%v codec=%q", ok, codec)
	}
	if ok, _ := parseNegotiatedSDP("v=0\r\nm=video 9000 RTP/AVP 96\r\n"); ok {
		t.Fatal("video-only SDP counted as negotiated audio")
	}
	if cause, reason := parseQ850Reason(`Q.850;cause=16;text="NORMAL_CLEARING"`); cause != 16 || reason != "NORMAL_CLEARING" {
		t.Fatalf("Q.850 parse: cause=%d reason=%q", cause, reason)
	}
}

func TestRTPDatagramNeedsRTPVersionAndMediaPort(t *testing.T) {
	rtp := &layers.UDP{SrcPort: 5060, DstPort: 12000, Payload: []byte{0x80, 0x00, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1}}
	if !isRTPDatagram(rtp) {
		t.Fatal("valid RTP packet was missed")
	}
	rtp.Payload[0] = 0x40
	if isRTPDatagram(rtp) {
		t.Fatal("non-RTP payload counted as RTP")
	}
	rtp.Payload[0] = 0x80
	rtp.DstPort = 5060
	if isRTPDatagram(rtp) {
		t.Fatal("SIP port counted as RTP")
	}
	if isRTPDatagram(nil) {
		t.Fatal("nil UDP packet accepted")
	}
}

func TestCallTCPReassemblyPreservesINVITEAndSDPBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.pcap")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := pcapgo.NewWriter(file)
	if err := writer.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatal(err)
	}
	clientIP, serverIP := net.ParseIP("192.0.2.10"), net.ParseIP("198.51.100.20")
	clientPort, serverPort := layers.TCPPort(40000), layers.TCPPort(5060)
	start := time.Now().Add(-time.Second).Round(time.Microsecond)
	stamp := start
	clientSeq, serverSeq := uint32(1000), uint32(5000)
	writeSyntheticTCPPacket(t, writer, clientIP, serverIP, clientPort, serverPort, clientSeq, 0, true, false, nil, stamp)
	clientSeq++
	stamp = stamp.Add(time.Millisecond)
	writeSyntheticTCPPacket(t, writer, serverIP, clientIP, serverPort, clientPort, serverSeq, clientSeq, true, true, nil, stamp)
	serverSeq++
	stamp = stamp.Add(time.Millisecond)
	writeSyntheticTCPPacket(t, writer, clientIP, serverIP, clientPort, serverPort, clientSeq, serverSeq, false, true, nil, stamp)
	stamp = stamp.Add(time.Millisecond)
	invite := "INVITE sip:+5567981340687@98034.falepaco.com.br:5060;transport=tcp SIP/2.0\r\nCall-ID: split-invite\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n"
	writeSplitPayload(t, writer, clientIP, serverIP, clientPort, serverPort, &clientSeq, serverSeq, []byte(invite), &stamp)
	sdp := "v=0\r\nm=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	response := "SIP/2.0 183 Session Progress\r\nCall-ID: split-invite\r\nCSeq: 1 INVITE\r\nContent-Type: application/sdp\r\nContent-Length: " + fmt.Sprint(len(sdp)) + "\r\n\r\n" + sdp
	writeSplitPayload(t, writer, serverIP, clientIP, serverPort, clientPort, &serverSeq, clientSeq, []byte(response), &stamp)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var messages []sipPCAPMessage
	_, err = parseSIPPCAPWithMessages(path, start.Add(2*time.Millisecond), "100", "test-secret", &messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].method != "INVITE" || !strings.Contains(string(messages[1].body), "PCMU/8000") {
		t.Fatalf("TCP reassembly missed call SIP/SDP: %+v", messages)
	}
}
