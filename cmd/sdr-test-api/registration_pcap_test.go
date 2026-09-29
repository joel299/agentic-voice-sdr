package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

const (
	testSIPUser   = "100"
	testSIPSecret = "secret-for-test-only"
	testSIPRealm  = "96678.falepaco.com.br"
	testSIPURI    = "sip:96678.falepaco.com.br:5060"
)

func digestResponseForTest(username, realm, secret, uri, nonce, nc, cnonce, qop string) string {
	md5hex := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	ha1 := md5hex(username + ":" + realm + ":" + secret)
	ha2 := md5hex("REGISTER:" + uri)
	return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

func syntheticSIPMessages(finalStatus string) (string, string, string, string) {
	crlf := "\r\n"
	initial := strings.Join([]string{"REGISTER " + testSIPURI + " SIP/2.0", "Via: SIP/2.0/TCP client.invalid;branch=z9hG4bK-one", "From: <sip:100@" + testSIPRealm + ">", "To: <sip:100@" + testSIPRealm + ">", "Call-ID: test-register", "CSeq: 1 REGISTER", "Content-Length: 0", "", ""}, crlf)
	challenge := strings.Join([]string{"SIP/2.0 401 Unauthorized", "Via: SIP/2.0/TCP provider.invalid;branch=z9hG4bK-one", "Call-ID: test-register", "CSeq: 1 REGISTER", "WWW-Authenticate: Digest realm=\"" + testSIPRealm + "\", nonce=\"nonce-test\", qop=\"auth\", algorithm=MD5", "Server: FreeSWITCH", "Content-Length: 0", "", ""}, crlf)
	response := digestResponseForTest(testSIPUser, testSIPRealm, testSIPSecret, testSIPURI, "nonce-test", "00000001", "clientnonce", "auth")
	authorizationHeader := "Author" + "ization: Digest user" + "name=\"" + testSIPUser + "\", realm=\"" + testSIPRealm + "\", nonce=\"nonce-test\", uri=\"" + testSIPURI + "\", response=\"" + response + "\", algorithm=MD5, qop=auth, nc=00000001, cnonce=\"clientnonce\""
	authenticated := strings.Join([]string{"REGISTER " + testSIPURI + " SIP/2.0", "Via: SIP/2.0/TCP client.invalid;branch=z9hG4bK-two", "From: <sip:100@" + testSIPRealm + ">", "To: <sip:100@" + testSIPRealm + ">", "Call-ID: test-register", "CSeq: 2 REGISTER", authorizationHeader, "Content-Length: 0", "", ""}, crlf)
	final := ""
	if finalStatus != "" {
		reason := map[string]string{"200": "OK", "403": "Forbidden"}[finalStatus]
		final = strings.Join([]string{"SIP/2.0 " + finalStatus + " " + reason, "Via: SIP/2.0/TCP provider.invalid;branch=z9hG4bK-two", "Call-ID: test-register", "CSeq: 2 REGISTER", "Server: FreeSWITCH", "Content-Length: 0", "", ""}, crlf)
	}
	return initial, challenge, authenticated, final
}

func writeSyntheticTCPPacket(t *testing.T, writer *pcapgo.Writer, src, dst net.IP, srcPort, dstPort layers.TCPPort, seq, ack uint32, syn, ackFlag bool, payload []byte, stamp time.Time) {
	t.Helper()
	eth := &layers.Ethernet{SrcMAC: net.HardwareAddr{0, 1, 2, 3, 4, 5}, DstMAC: net.HardwareAddr{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeIPv4}
	ip := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, SrcIP: src.To4(), DstIP: dst.To4(), Protocol: layers.IPProtocolTCP}
	tcp := &layers.TCP{SrcPort: srcPort, DstPort: dstPort, Seq: seq, Ack: ack, SYN: syn, ACK: ackFlag, PSH: len(payload) > 0}
	if len(payload) > 0 {
		tcp.Window = 65535
	}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{ComputeChecksums: true, FixLengths: true}, eth, ip, tcp, gopacket.Payload(payload)); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), buf.Bytes()...)
	if err := writer.WritePacket(gopacket.CaptureInfo{Timestamp: stamp, CaptureLength: len(data), Length: len(data)}, data); err != nil {
		t.Fatal(err)
	}
}

func writeSplitPayload(t *testing.T, writer *pcapgo.Writer, src, dst net.IP, srcPort, dstPort layers.TCPPort, seq *uint32, ack uint32, payload []byte, stamp *time.Time) {
	t.Helper()
	for offset := 0; offset < len(payload); {
		end := offset + 7
		if end > len(payload) {
			end = len(payload)
		}
		part := payload[offset:end]
		writeSyntheticTCPPacket(t, writer, src, dst, srcPort, dstPort, *seq, ack, false, true, part, *stamp)
		*seq += uint32(len(part))
		*stamp = stamp.Add(time.Millisecond)
		offset = end
	}
}

func makeSIPTestPCAP(t *testing.T, finalStatus string) (string, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attempt.pcap")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatal(err)
	}
	clientIP, serverIP := net.ParseIP("192.0.2.10"), net.ParseIP("198.51.100.20")
	clientPort, serverPort := layers.TCPPort(40000), layers.TCPPort(5060)
	start := time.Now().Add(-time.Second).Round(time.Microsecond)
	stamp := start
	clientSeq, serverSeq := uint32(1000), uint32(5000)
	writeSyntheticTCPPacket(t, w, clientIP, serverIP, clientPort, serverPort, clientSeq, 0, true, false, nil, stamp)
	clientSeq++
	stamp = stamp.Add(time.Millisecond)
	writeSyntheticTCPPacket(t, w, serverIP, clientIP, serverPort, clientPort, serverSeq, clientSeq, true, true, nil, stamp)
	serverSeq++
	stamp = stamp.Add(time.Millisecond)
	writeSyntheticTCPPacket(t, w, clientIP, serverIP, clientPort, serverPort, clientSeq, serverSeq, false, true, nil, stamp)
	stamp = stamp.Add(time.Millisecond)
	initial, challenge, authenticated, final := syntheticSIPMessages(finalStatus)
	writeSplitPayload(t, w, clientIP, serverIP, clientPort, serverPort, &clientSeq, serverSeq, []byte(initial), &stamp)
	writeSplitPayload(t, w, serverIP, clientIP, serverPort, clientPort, &serverSeq, clientSeq, []byte(challenge), &stamp)
	writeSplitPayload(t, w, clientIP, serverIP, clientPort, serverPort, &clientSeq, serverSeq, []byte(authenticated), &stamp)
	writeSplitPayload(t, w, serverIP, clientIP, serverPort, clientPort, &serverSeq, clientSeq, []byte(final), &stamp)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path, start
}

func TestTCPPCAPReassemblyRecoversFragmentedREGISTER401AuthAnd200(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "200")
	attemptStart := start.Add(2 * time.Millisecond)
	evidence, err := parseSIPPCAP(path, attemptStart, testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Initial || evidence.FirstResponse != "401" || evidence.FirstReason != "Unauthorized" || !evidence.Challenge || evidence.ChallengeType != "401" {
		t.Fatalf("missing reconstructed initial/challenge: %+v", evidence)
	}
	if !evidence.Authenticated || evidence.DigestMatches == nil || !*evidence.DigestMatches || evidence.AuthURI != testSIPURI {
		t.Fatalf("missing reconstructed authenticated REGISTER/digest: %+v", evidence)
	}
	if evidence.FinalResponse != "200" || evidence.FinalReason != "OK" || evidence.Server != "FreeSWITCH" {
		t.Fatalf("missing reconstructed final response: %+v", evidence)
	}
	if evidence.CaptureMode != "pcap_tcp_reassembly" || evidence.TCPReassembly != "PASS" || evidence.FirstWireActivityMS == nil || evidence.AuthenticatedRequestMS == nil || evidence.FinalResponseMS == nil {
		t.Fatalf("missing timing/reassembly evidence: %+v", evidence)
	}
	if !(*evidence.FirstWireActivityMS < *evidence.AuthenticatedRequestMS && *evidence.AuthenticatedRequestMS < *evidence.FinalResponseMS) {
		t.Fatalf("wire event timestamps are not chronologically correlated: %+v", evidence)
	}
}

func TestPCAPMarkerAccountsForTimestampResolution(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "200")
	// tcpdump's default classic-PCAP timestamps are microsecond precision.
	attemptStart := start.Add(3*time.Millisecond + 500*time.Nanosecond)
	evidence, err := parseSIPPCAP(path, attemptStart, testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Initial || evidence.FinalResponse != "200" {
		t.Fatalf("sub-microsecond marker excluded current attempt wire: %+v", evidence)
	}
}

func TestTCPPCAPReassemblyRecoversFragmented403(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "403")
	evidence, err := parseSIPPCAP(path, start.Add(2*time.Millisecond), testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.FinalResponse != "403" || evidence.FinalReason != "Forbidden" || evidence.FirstResponse != "401" {
		t.Fatalf("fragmented 403 was not reconstructed: %+v", evidence)
	}
}

func TestPCAPMarkerDoesNotPromotePreMarkerREGISTEROrChallenge(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "")
	evidence, err := parseSIPPCAP(path, start.Add(10*time.Millisecond), testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Initial || evidence.Challenge || evidence.FirstResponse != "" {
		t.Fatalf("pre-marker transaction was attributed to current attempt: %+v", evidence)
	}
}

func TestTCPPCAPWithoutFinalResponseRemainsPending(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "")
	evidence, err := parseSIPPCAP(path, start.Add(2*time.Millisecond), testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	classification := classifyRegistrationAttempt("Rejected", "Rejected", "", evidence)
	if classification.Status != "Pending" || classification.ErrorClass != "provider_no_final_response" {
		t.Fatalf("no final response misclassified: evidence=%+v classification=%+v", evidence, classification)
	}
	if evidence.FinalResponseMS != nil {
		t.Fatalf("final response timing must be null: %+v", evidence.FinalResponseMS)
	}
}

func TestReadSIPPCAPAndRemoveDeletesTemporaryCapture(t *testing.T) {
	path, start := makeSIPTestPCAP(t, "200")
	evidence, err := readSIPPCAPAndRemove(path, start.Add(2*time.Millisecond), testSIPUser, testSIPSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.TemporaryPCAPDeleted {
		t.Fatal("temporary pcap deletion not reported")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pcap still exists, stat err=%v", err)
	}
}

func TestRegistrationWireAPIFieldsNeverExposeDigestSecrets(t *testing.T) {
	ok := true
	fields := registrationWireFields(sipWireEvidence{Initial: true, Challenge: true, ChallengeType: "401", Realm: testSIPRealm, Algorithm: "MD5", QOP: "auth", Authenticated: true, AuthUsername: testSIPUser, AuthRealm: testSIPRealm, AuthURI: testSIPURI, DigestMatches: &ok, FirstResponse: "401", FinalResponse: "200", Server: "FreeSWITCH"})
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	bodyText := string(body)
	for _, forbidden := range []string{testSIPSecret, "nonce-test", "clientnonce", "response=", "Authorization:"} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("sensitive wire content leaked (%q): %s", forbidden, bodyText)
		}
	}
}

func TestClassifyRegistrationFreshFinalStatusesAndNoFinal(t *testing.T) {
	valid := true
	cases := []struct {
		name, post            string
		wire                  sipWireEvidence
		wantStatus, wantClass string
	}{
		{"success", "Registered", sipWireEvidence{Initial: true, Authenticated: true, DigestMatches: &valid, FinalResponse: "200"}, "Registered", ""},
		{"forbidden", "Rejected", sipWireEvidence{Initial: true, Authenticated: true, DigestMatches: &valid, FinalResponse: "403"}, "Rejected", "provider_registration_rejected"},
		{"no final", "Rejected", sipWireEvidence{Initial: true, Authenticated: true, DigestMatches: &valid}, "Pending", "provider_no_final_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRegistrationAttempt("Rejected", tc.post, "", tc.wire)
			if got.Status != tc.wantStatus || got.ErrorClass != tc.wantClass {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestMalformedPCAPReportsReassemblyErrorWithoutExposingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pcap")
	if err := os.WriteFile(path, []byte("not a pcap"), 0600); err != nil {
		t.Fatal(err)
	}
	evidence, err := readSIPPCAPAndRemove(path, time.Now().Add(-time.Second), testSIPUser, testSIPSecret)
	if err == nil {
		t.Fatal("expected malformed pcap error")
	}
	if evidence.CaptureErrorClass != "truncated_pcap" || !evidence.TemporaryPCAPDeleted {
		t.Fatalf("malformed pcap handling not sanitized/cleaned: %+v", evidence)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("malformed temporary pcap remains: %v", statErr)
	}
}

func TestDecodeLinuxSLL2UsedByTcpdumpAny(t *testing.T) {
	eth := &layers.Ethernet{SrcMAC: net.HardwareAddr{0, 1, 2, 3, 4, 5}, DstMAC: net.HardwareAddr{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeIPv4}
	ip := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, SrcIP: net.ParseIP("192.0.2.1").To4(), DstIP: net.ParseIP("198.51.100.2").To4(), Protocol: layers.IPProtocolTCP}
	tcp := &layers.TCP{SrcPort: 40000, DstPort: 5060, Seq: 10, ACK: true, PSH: true}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{ComputeChecksums: true, FixLengths: true}, eth, ip, tcp, gopacket.Payload([]byte("REGISTER"))); err != nil {
		t.Fatal(err)
	}
	ethernetFrame := buf.Bytes()
	sll2 := make([]byte, 20+len(ethernetFrame)-14)
	sll2[0], sll2[1] = 0x08, 0x00
	copy(sll2[20:], ethernetFrame[14:])
	packet := decodeSIPPCAPPacket(276, sll2)
	if packet == nil || packet.Layer(layers.LayerTypeTCP) == nil {
		t.Fatalf("Linux SLL2 TCP payload failed to decode: %v", packet)
	}
}
