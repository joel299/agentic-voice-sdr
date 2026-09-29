package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/google/gopacket/tcpassembly"
)

const maxRegistrationPCAPBytes = 48 << 20
const maxRegistrationTCPPackets = 30000

type sipPCAPMessage struct {
	startLine  string
	headers    map[string]string
	requestURI string
	offset     int
	firstSeen  time.Time
	seen       time.Time
}

type sipCaptureSpan struct {
	length int
	seen   time.Time
}

type sipTCPStream struct {
	factory *sipTCPStreamFactory
	data    []byte
	spans   []sipCaptureSpan
}

func (s *sipTCPStream) Reassembled(parts []tcpassembly.Reassembly) {
	for _, part := range parts {
		if part.Skip > 0 {
			if !part.Seen.Before(s.factory.attemptStart) {
				s.factory.gap = true
			}
			s.data, s.spans = nil, nil
		}
		if len(part.Bytes) == 0 {
			continue
		}
		s.data = append(s.data, part.Bytes...)
		if len(s.spans) > 0 && s.spans[len(s.spans)-1].seen.Equal(part.Seen) {
			s.spans[len(s.spans)-1].length += len(part.Bytes)
		} else {
			s.spans = append(s.spans, sipCaptureSpan{length: len(part.Bytes), seen: part.Seen})
		}
		for {
			message, rest, complete := takeSIPMessage(s.data)
			if !complete {
				break
			}
			consumed := len(s.data) - len(rest)
			message.firstSeen = sipSpanTime(s.spans, message.offset)
			message.seen = sipSpanTime(s.spans, consumed-1)
			s.data, s.spans = rest, consumeSIPSpans(s.spans, consumed)
			s.factory.messages = append(s.factory.messages, message)
		}
		if len(s.data) > 1<<20 {
			if len(s.spans) > 0 && !sipSpanTime(s.spans, 0).Before(s.factory.attemptStart) {
				s.factory.incompleteMessage = true
			}
			s.data, s.spans = nil, nil
		}
	}
}

func sipSpanTime(spans []sipCaptureSpan, offset int) time.Time {
	if offset < 0 {
		return time.Time{}
	}
	for _, span := range spans {
		if offset < span.length {
			return span.seen
		}
		offset -= span.length
	}
	return time.Time{}
}

func consumeSIPSpans(spans []sipCaptureSpan, count int) []sipCaptureSpan {
	for count > 0 && len(spans) > 0 {
		if count >= spans[0].length {
			count -= spans[0].length
			spans = spans[1:]
			continue
		}
		spans[0].length -= count
		count = 0
	}
	return spans
}

func (s *sipTCPStream) ReassemblyComplete() {}

type sipTCPStreamFactory struct {
	attemptStart      time.Time
	streams           []*sipTCPStream
	messages          []sipPCAPMessage
	gap               bool
	incompleteMessage bool
}

func (f *sipTCPStreamFactory) New(_, _ gopacket.Flow) tcpassembly.Stream {
	stream := &sipTCPStream{factory: f}
	f.streams = append(f.streams, stream)
	return stream
}

func takeSIPMessage(data []byte) (sipPCAPMessage, []byte, bool) {
	var empty sipPCAPMessage
	startOffset := 0
	for len(data) > 0 {
		if !hasSIPStartPrefix(data) {
			if i := nextSIPStart(data); i >= 0 {
				startOffset += i
				data = data[i:]
			} else {
				keep := possibleSIPPrefixSuffix(data)
				return empty, append([]byte(nil), data[len(data)-keep:]...), false
			}
		}
		headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
		if headerEnd < 0 {
			return empty, data, false
		}
		headerBlock := string(data[:headerEnd])
		lines := strings.Split(headerBlock, "\r\n")
		if len(lines) == 0 || !(strings.HasPrefix(lines[0], "REGISTER ") || strings.HasPrefix(lines[0], "SIP/2.0 ")) {
			data = data[1:]
			startOffset++
			continue
		}
		headers := make(map[string]string)
		lastKey := ""
		for _, line := range lines[1:] {
			if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && lastKey != "" {
				headers[lastKey] += " " + strings.TrimSpace(line)
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			lastKey = strings.ToLower(strings.TrimSpace(key))
			headers[lastKey] = strings.TrimSpace(value)
		}
		bodyLength := 0
		if rawLength := headers["content-length"]; rawLength != "" {
			parsed, err := strconv.Atoi(strings.TrimSpace(rawLength))
			if err != nil || parsed < 0 || parsed > 1<<20 {
				return empty, nil, false
			}
			bodyLength = parsed
		}
		total := headerEnd + 4 + bodyLength
		if len(data) < total {
			return empty, data, false
		}
		message := sipPCAPMessage{startLine: lines[0], headers: headers, offset: startOffset}
		if strings.HasPrefix(lines[0], "REGISTER ") {
			parts := strings.Fields(lines[0])
			if len(parts) > 1 {
				message.requestURI = parts[1]
			}
		}
		return message, append([]byte(nil), data[total:]...), true
	}
	return empty, data, false
}

func hasSIPStartPrefix(data []byte) bool {
	for _, prefix := range [][]byte{[]byte("REGISTER "), []byte("SIP/2.0 ")} {
		if bytes.HasPrefix(data, prefix) || bytes.HasPrefix(prefix, data) {
			return true
		}
	}
	return false
}

func nextSIPStart(data []byte) int {
	register := bytes.Index(data, []byte("REGISTER "))
	response := bytes.Index(data, []byte("SIP/2.0 "))
	if register < 0 {
		return response
	}
	if response < 0 || register < response {
		return register
	}
	return response
}

func possibleSIPPrefixSuffix(data []byte) int {
	maxKeep := 0
	for _, prefix := range []string{"REGISTER ", "SIP/2.0 "} {
		limit := len(prefix) - 1
		if limit > len(data) {
			limit = len(data)
		}
		for size := 1; size <= limit; size++ {
			if bytes.Equal(data[len(data)-size:], []byte(prefix[:size])) && size > maxKeep {
				maxKeep = size
			}
		}
	}
	return maxKeep
}

func appendUDPSIPMessages(factory *sipTCPStreamFactory, payload []byte, stamp time.Time) {
	remaining := append([]byte(nil), payload...)
	for len(remaining) > 0 {
		message, rest, complete := takeSIPMessage(remaining)
		if !complete {
			if hasSIPStartPrefix(remaining) && !stamp.Before(factory.attemptStart) {
				factory.incompleteMessage = true
			}
			return
		}
		message.firstSeen, message.seen = stamp, stamp
		factory.messages = append(factory.messages, message)
		if len(rest) == len(remaining) {
			return
		}
		remaining = rest
	}
}

func decodeSIPPCAPPacket(linkType uint32, data []byte) gopacket.Packet {
	switch linkType {
	case 1:
		return gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.NoCopy)
	case 113:
		return gopacket.NewPacket(data, layers.LayerTypeLinuxSLL, gopacket.NoCopy)
	case 101:
		if len(data) == 0 {
			return nil
		}
		if data[0]>>4 == 4 {
			return gopacket.NewPacket(data, layers.LayerTypeIPv4, gopacket.NoCopy)
		}
		if data[0]>>4 == 6 {
			return gopacket.NewPacket(data, layers.LayerTypeIPv6, gopacket.NoCopy)
		}
	case 228:
		return gopacket.NewPacket(data, layers.LayerTypeIPv4, gopacket.NoCopy)
	case 229:
		return gopacket.NewPacket(data, layers.LayerTypeIPv6, gopacket.NoCopy)
	case 276: // Linux cooked capture v2, used by tcpdump -i any.
		if len(data) < 20 {
			return nil
		}
		switch uint16(data[0])<<8 | uint16(data[1]) {
		case 0x0800:
			return gopacket.NewPacket(data[20:], layers.LayerTypeIPv4, gopacket.NoCopy)
		case 0x86dd:
			return gopacket.NewPacket(data[20:], layers.LayerTypeIPv6, gopacket.NoCopy)
		}
	}
	return nil
}

func pcapTimestampResolution(data []byte) (time.Duration, error) {
	if len(data) < 4 {
		return 0, errors.New("pcap header truncated")
	}
	switch binary.BigEndian.Uint32(data[:4]) {
	case 0xd4c3b2a1, 0xa1b2c3d4:
		return time.Microsecond, nil
	case 0x4d3cb2a1, 0xa1b23c4d:
		return time.Nanosecond, nil
	default:
		return 0, errors.New("invalid pcap magic")
	}
}

func pcapLinkType(data []byte) (uint32, error) {
	if len(data) < 24 {
		return 0, errors.New("pcap header truncated")
	}
	var order binary.ByteOrder
	switch string(data[:4]) {
	case "\xd4\xc3\xb2\xa1", "\x4d\x3c\xb2\xa1":
		order = binary.LittleEndian
	case "\xa1\xb2\xc3\xd4", "\xa1\xb2\x3c\x4d":
		order = binary.BigEndian
	default:
		return 0, errors.New("invalid pcap magic")
	}
	return order.Uint32(data[20:24]) & 0xffff, nil
}

func parseSIPPCAP(path string, attemptStart time.Time, username, secret string) (sipWireEvidence, error) {
	var evidence sipWireEvidence
	if attemptStart.IsZero() {
		return evidence, errors.New("attempt start time missing")
	}
	stat, err := os.Stat(path)
	if err != nil {
		return evidence, err
	}
	if stat.Size() <= 24 {
		return evidence, errors.New("pcap empty or truncated")
	}
	if stat.Size() > maxRegistrationPCAPBytes {
		return evidence, errors.New("pcap exceeds bounded size")
	}
	rawPCAP, err := os.ReadFile(path)
	if err != nil {
		return evidence, err
	}
	linkType, err := pcapLinkType(rawPCAP)
	if err != nil {
		return evidence, err
	}
	reader, err := pcapgo.NewReader(bytes.NewReader(rawPCAP))
	if err != nil {
		return evidence, fmt.Errorf("invalid pcap: %w", err)
	}
	resolution, err := pcapTimestampResolution(rawPCAP)
	if err != nil {
		return evidence, err
	}
	captureMarker := attemptStart.Truncate(resolution)
	factory := &sipTCPStreamFactory{attemptStart: captureMarker}
	assembler := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(factory))
	var packetCount int
	var capturePacketsPresent, sawTCP, sawUDP bool
	var firstWireActivity time.Time
	var sourceIPPort, remoteIPPort string
	for {
		data, ci, readErr := reader.ReadPacketData()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return evidence, fmt.Errorf("truncated pcap: %w", readErr)
		}
		packetCount++
		if packetCount > maxRegistrationTCPPackets {
			return evidence, errors.New("pcap packet limit exceeded")
		}
		packet := decodeSIPPCAPPacket(linkType, data)
		if packet == nil {
			return evidence, errors.New("unsupported pcap link type")
		}
		if udpLayer := packet.Layer(layers.LayerTypeUDP); udpLayer != nil {
			udp := udpLayer.(*layers.UDP)
			sawUDP = true
			if !ci.Timestamp.Before(captureMarker) && len(udp.Payload) > 0 {
				capturePacketsPresent = true
				if firstWireActivity.IsZero() || ci.Timestamp.Before(firstWireActivity) {
					firstWireActivity = ci.Timestamp
				}
				appendUDPSIPMessages(factory, udp.Payload, ci.Timestamp)
			}
			continue
		}
		tcpLayer := packet.Layer(layers.LayerTypeTCP)
		if tcpLayer == nil {
			continue
		}
		network := packet.NetworkLayer()
		if network == nil {
			continue
		}
		tcp, ok := tcpLayer.(*layers.TCP)
		if !ok {
			continue
		}
		sawTCP = true
		if !ci.Timestamp.Before(captureMarker) && len(tcp.Payload) > 0 {
			capturePacketsPresent = true
			if sourceIPPort == "" {
				sourceIPPort = net.JoinHostPort(network.NetworkFlow().Src().String(), strconv.Itoa(int(tcp.SrcPort)))
				remoteIPPort = net.JoinHostPort(network.NetworkFlow().Dst().String(), strconv.Itoa(int(tcp.DstPort)))
			}
			if firstWireActivity.IsZero() || ci.Timestamp.Before(firstWireActivity) {
				firstWireActivity = ci.Timestamp
			}
		}
		assembler.AssembleWithTimestamp(network.NetworkFlow(), tcp, ci.Timestamp)
	}
	assembler.FlushAll()
	for _, stream := range factory.streams {
		if len(stream.data) > 0 && len(stream.spans) > 0 && !sipSpanTime(stream.spans, 0).Before(captureMarker) && hasSIPStartPrefix(stream.data) {
			factory.incompleteMessage = true
		}
	}
	evidence.CapturePacketsPresent = capturePacketsPresent && packetCount > 0
	evidence.SourceIPPort, evidence.RemoteIPPort = sourceIPPort, remoteIPPort
	if sawTCP {
		evidence.CaptureMode, evidence.TCPReassembly = "pcap_tcp_reassembly", "PASS"
	}
	if sawUDP && !sawTCP {
		evidence.CaptureMode, evidence.TCPReassembly = "pcap_udp_datagrams", "NOT_REQUIRED"
	}
	if factory.gap {
		evidence.CaptureErrorClass = "tcp_reassembly_gap"
		evidence.TCPReassembly = "FAIL"
	}
	if factory.incompleteMessage {
		evidence.CaptureErrorClass = "tcp_reassembly_incomplete_message"
		if sawTCP {
			evidence.TCPReassembly = "FAIL"
		}
	}
	if !firstWireActivity.IsZero() {
		evidence.FirstWireActivityMS = durationMillisPtr(firstWireActivity.Sub(attemptStart))
	}
	messages := append([]sipPCAPMessage(nil), factory.messages...)
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].seen.Before(messages[j].seen) })
	var currentCallID, currentCSeq string
	for _, message := range messages {
		if message.firstSeen.IsZero() || message.firstSeen.Before(captureMarker) {
			continue
		}
		line := strings.TrimSpace(message.startLine)
		if strings.HasPrefix(line, "REGISTER ") {
			callID := strings.TrimSpace(message.headers["call-id"])
			cseq := sipCSeqSequence(message.headers["cseq"])
			if currentCallID == "" {
				currentCallID = callID
			}
			if callID == currentCallID && currentCallID != "" {
				currentCSeq = cseq
			}
			auth, hasAuthorization := message.headers["authorization"]
			proxyAuth, hasProxyAuthorization := message.headers["proxy-authorization"]
			if !hasAuthorization && !hasProxyAuthorization {
				evidence.Initial = true
				evidence.RequestURI = message.requestURI
				evidence.FromURI = sipHeaderURI(message.headers["from"])
				evidence.ToURI = sipHeaderURI(message.headers["to"])
				evidence.ContactURI = sipHeaderURI(message.headers["contact"])
				evidence.ViaSentBy = sipViaSentBy(message.headers["via"])
				evidence.Expires = sanitizeSIPHeader(message.headers["expires"])
				evidence.RouteURI = sipHeaderURI(message.headers["route"])
			}
			if callID == currentCallID && currentCallID != "" && (hasAuthorization || hasProxyAuthorization) {
				evidence.Authenticated = true
				if hasAuthorization {
					proxyAuth = auth
				}
				fields := parseDigestFields(proxyAuth)
				evidence.AuthUsername, evidence.AuthRealm, evidence.AuthURI = fields["username"], fields["realm"], fields["uri"]
				valid := digestMatchesSIP(evidence.AuthUsername, evidence.AuthRealm, secret, "REGISTER", fields)
				evidence.DigestMatches = &valid
				evidence.AuthenticatedRequestMS = durationMillisPtr(message.seen.Sub(attemptStart))
			}
			continue
		}
		if !strings.HasPrefix(line, "SIP/2.0 ") {
			continue
		}
		if currentCallID == "" || strings.TrimSpace(message.headers["call-id"]) != currentCallID || sipCSeqSequence(message.headers["cseq"]) != currentCSeq {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(line, "SIP/2.0 "), " ", 2)
		code := parts[0]
		reason := ""
		if len(parts) > 1 {
			reason = parts[1]
		}
		if evidence.FirstResponse == "" {
			evidence.FirstResponse, evidence.FirstReason = code, reason
		}
		if challenge, ok := message.headers["www-authenticate"]; ok && (code == "401" || code == "407") {
			evidence.Challenge, evidence.ChallengeType = true, code
			fields := parseDigestFields(challenge)
			evidence.Realm, evidence.Algorithm, evidence.QOP = fields["realm"], fields["algorithm"], fields["qop"]
		}
		if challenge, ok := message.headers["proxy-authenticate"]; ok && (code == "401" || code == "407") {
			evidence.Challenge, evidence.ChallengeType = true, code
			fields := parseDigestFields(challenge)
			evidence.Realm, evidence.Algorithm, evidence.QOP = fields["realm"], fields["algorithm"], fields["qop"]
		}
		statusCode, _ := strconv.Atoi(code)
		if statusCode >= 200 && code != "401" && code != "407" {
			evidence.FinalResponse, evidence.FinalReason = code, reason
			if warning, ok := message.headers["warning"]; ok {
				evidence.FinalWarningHeaderPresent, evidence.FinalWarningHeader = true, sanitizeSIPHeader(warning)
			}
			if headerReason, ok := message.headers["reason"]; ok {
				evidence.FinalReasonHeaderPresent, evidence.FinalReasonHeader = true, sanitizeSIPHeader(headerReason)
			}
			evidence.FinalResponseMS = durationMillisPtr(message.seen.Sub(attemptStart))
		}
		if server := message.headers["server"]; server != "" {
			evidence.Server = server
		}
		if userAgent := message.headers["user-agent"]; userAgent != "" {
			evidence.Server = userAgent
		}
	}
	return evidence, nil
}

func sipHeaderURI(value string) string {
	start, end := strings.Index(value, "<"), strings.Index(value, ">")
	if start >= 0 && end > start {
		return sanitizeSIPHeader(value[start+1 : end])
	}
	value = strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return sanitizeSIPHeader(strings.Trim(value, "<> \\t\\\""))
}

func sipViaSentBy(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return ""
	}
	return sanitizeSIPHeader(strings.SplitN(fields[1], ";", 2)[0])
}

func sanitizeSIPHeader(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func sipCSeqSequence(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func durationMillisPtr(d time.Duration) *int64 {
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

func readSIPPCAPAndRemove(path string, attemptStart time.Time, username, secret string) (evidence sipWireEvidence, err error) {
	defer func() {
		removeErr := os.Remove(path)
		evidence.TemporaryPCAPDeleted = removeErr == nil || errors.Is(removeErr, os.ErrNotExist)
		if err == nil && !evidence.TemporaryPCAPDeleted {
			err = errors.New("temporary pcap cleanup failed")
		}
	}()
	evidence, err = parseSIPPCAP(path, attemptStart, username, secret)
	if err != nil {
		evidence.CaptureMode = "pcap"
		evidence.TCPReassembly = "FAIL"
		evidence.CaptureErrorClass = classifyRegistrationPCAPError(err)
	}
	return evidence, err
}

func classifyRegistrationPCAPError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "exceeds bounded size") || strings.Contains(text, "packet limit"):
		return "capture_limit_exceeded"
	case strings.Contains(text, "truncated") || strings.Contains(text, "unexpected eof"):
		return "truncated_pcap"
	case strings.Contains(text, "invalid pcap") || strings.Contains(text, "unsupported link type"):
		return "invalid_pcap"
	case strings.Contains(text, "tcp_reassembly"):
		return text
	default:
		return "tcp_reassembly_failed"
	}
}

func registrationWireFields(wire sipWireEvidence) map[string]any {
	return map[string]any{
		"register_request_uri":                       wire.RequestURI,
		"register_from_uri":                          wire.FromURI,
		"register_to_uri":                            wire.ToURI,
		"register_contact_uri":                       wire.ContactURI,
		"register_via_sent_by":                       wire.ViaSentBy,
		"register_expires":                           wire.Expires,
		"register_route_uri":                         wire.RouteURI,
		"register_source_ip_port":                    wire.SourceIPPort,
		"register_remote_ip_port":                    wire.RemoteIPPort,
		"final_warning_header_present":               wire.FinalWarningHeaderPresent,
		"final_warning_header":                       wire.FinalWarningHeader,
		"final_reason_header_present":                wire.FinalReasonHeaderPresent,
		"final_reason_header":                        wire.FinalReasonHeader,
		"registration_initial_request_present":       wire.Initial,
		"registration_first_response":                wire.FirstResponse,
		"registration_first_reason":                  wire.FirstReason,
		"registration_challenge_received":            wire.Challenge,
		"registration_challenge_type":                wire.ChallengeType,
		"registration_realm":                         wire.Realm,
		"registration_algorithm":                     wire.Algorithm,
		"registration_qop":                           wire.QOP,
		"registration_authenticated_request_sent":    wire.Authenticated,
		"registration_auth_username":                 wire.AuthUsername,
		"registration_auth_realm":                    wire.AuthRealm,
		"registration_auth_uri":                      wire.AuthURI,
		"registration_digest_matches_runtime_secret": wire.DigestMatches,
		"registration_final_response":                wire.FinalResponse,
		"registration_final_reason":                  wire.FinalReason,
		"provider_server_or_user_agent":              wire.Server,
		"registration_capture_mode":                  wire.CaptureMode,
		"registration_tcp_reassembly":                wire.TCPReassembly,
		"registration_capture_error_class":           wire.CaptureErrorClass,
		"registration_capture_window_ms":             wire.CaptureWindowMS,
		"registration_first_wire_activity_ms":        wire.FirstWireActivityMS,
		"registration_authenticated_request_ms":      wire.AuthenticatedRequestMS,
		"registration_final_response_ms":             wire.FinalResponseMS,
		"registration_temporary_pcap_deleted":        wire.TemporaryPCAPDeleted,
	}
}
