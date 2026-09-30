package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type sipCallEvidence struct {
	InviteSent, Challenge, Authenticated, DigestMatches bool
	Progress                                            []int
	FinalStatus                                         int
	FinalReason                                         string
	Established, SDPNegotiated                          bool
	Codec                                               string
	Q850Cause                                           int
	Q850Reason                                          string
	DurationSeconds                                     float64
}

func summarizeSIPCall(capture sipWireEvidence, messages []sipPCAPMessage, username, secret, expectedRealm string) sipCallEvidence {
	var out sipCallEvidence
	var callID, initialCSeq string
	var challengeRealm string
	var inviteStarted, callEnded time.Time
	inviteCSeqs := map[string]bool{}
	var sdp string
	for _, message := range messages {
		if message.method == "INVITE" {
			currentCallID := strings.TrimSpace(message.headers["call-id"])
			cseq := sipCSeqSequence(message.headers["cseq"])
			if callID == "" && message.headers["authorization"] == "" && message.headers["proxy-authorization"] == "" {
				callID, initialCSeq, inviteStarted = currentCallID, cseq, message.seen
				out.InviteSent = currentCallID != "" && message.requestURI != ""
			}
			if currentCallID == callID && callID != "" {
				inviteCSeqs[cseq] = true
				auth := message.headers["authorization"]
				if auth == "" {
					auth = message.headers["proxy-authorization"]
				}
				if auth != "" {
					fields := parseDigestFields(auth)
					out.Authenticated = true
					out.DigestMatches = fields["username"] == username && strings.EqualFold(fields["realm"], expectedRealm) && strings.EqualFold(challengeRealm, expectedRealm) && digestMatchesSIP(username, expectedRealm, secret, "INVITE", fields)
				}
			}
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(message.startLine), "SIP/2.0 ") || callID == "" || strings.TrimSpace(message.headers["call-id"]) != callID {
			continue
		}
		cseqLine := strings.Fields(strings.TrimSpace(message.headers["cseq"]))
		if len(cseqLine) < 2 {
			continue
		}
		cseq, method := cseqLine[0], strings.ToUpper(cseqLine[1])
		if len(cseqLine) >= 2 && method == "INVITE" && (cseq == initialCSeq || inviteCSeqs[cseq]) {
			code, reason := sipStatusFromLine(message.startLine)
			if code == 401 && message.headers["www-authenticate"] != "" || code == 407 && message.headers["proxy-authenticate"] != "" {
				out.Challenge = true
				header := message.headers["www-authenticate"]
				if header == "" {
					header = message.headers["proxy-authenticate"]
				}
				challengeRealm = parseDigestFields(header)["realm"]
			}
			if code == 100 || code == 180 || code == 183 {
				out.Progress = appendUniqueStatus(out.Progress, code)
			}
			if code >= 200 && code != 401 && code != 407 {
				out.FinalStatus, out.FinalReason = code, sanitizeSIPHeader(reason)
				if strings.Contains(strings.ToLower(string(message.body)), "m=audio") {
					sdp = string(message.body)
				}
			}
		}
		if method == "BYE" {
			if q850, reason := parseQ850Reason(message.headers["reason"]); q850 > 0 {
				out.Q850Cause, out.Q850Reason = q850, reason
			}
			callEnded = message.seen
		}
		if strings.HasPrefix(strings.TrimSpace(message.startLine), "SIP/2.0 ") && method == "BYE" {
			if q850, reason := parseQ850Reason(message.headers["reason"]); q850 > 0 {
				out.Q850Cause, out.Q850Reason = q850, reason
			}
			callEnded = message.seen
		}
	}
	out.Established = out.FinalStatus >= 200 && out.FinalStatus < 300 && hasCallACK(messages, callID)
	out.SDPNegotiated, out.Codec = parseNegotiatedSDP(sdp)
	if out.DurationSeconds == 0 && !inviteStarted.IsZero() && !callEnded.IsZero() && !callEnded.Before(inviteStarted) {
		out.DurationSeconds = callEnded.Sub(inviteStarted).Seconds()
	}
	if out.DurationSeconds == 0 && !inviteStarted.IsZero() {
		for _, message := range messages {
			if strings.TrimSpace(message.headers["call-id"]) == callID && strings.HasPrefix(message.startLine, "SIP/2.0 ") {
				if code, _ := sipStatusFromLine(message.startLine); code >= 200 && !message.seen.Before(inviteStarted) {
					out.DurationSeconds = message.seen.Sub(inviteStarted).Seconds()
				}
			}
		}
	}
	return out
}

func sipStatusFromLine(line string) (int, string) {
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 || parts[0] != "SIP/2.0" {
		return 0, ""
	}
	code, _ := strconv.Atoi(parts[1])
	if len(parts) == 3 {
		return code, parts[2]
	}
	return code, ""
}

func appendUniqueStatus(statuses []int, value int) []int {
	for _, existing := range statuses {
		if existing == value {
			return statuses
		}
	}
	return append(statuses, value)
}

func hasCallACK(messages []sipPCAPMessage, callID string) bool {
	if callID == "" {
		return false
	}
	for _, message := range messages {
		if message.method == "ACK" && strings.TrimSpace(message.headers["call-id"]) == callID {
			return true
		}
	}
	return false
}

var sdpCodecRE = regexp.MustCompile(`(?im)^a=rtpmap:\d+\s+([^/\s]+/\d+(?:/\d+)?)`)
var sdpAudioRE = regexp.MustCompile(`(?im)^m=audio\s+\d+\s+RTP/AVP\s+([0-9 ]+)`)

func parseNegotiatedSDP(sdp string) (bool, string) {
	if !strings.Contains(sdp, "v=") || !strings.Contains(strings.ToLower(sdp), "m=audio") {
		return false, ""
	}
	if match := sdpCodecRE.FindStringSubmatch(sdp); len(match) == 2 {
		codec := strings.ToUpper(match[1])
		return true, strings.TrimSuffix(codec, "/1")
	}
	if match := sdpAudioRE.FindStringSubmatch(sdp); len(match) == 2 {
		for _, payload := range strings.Fields(match[1]) {
			switch payload {
			case "0":
				return true, "PCMU/8000"
			case "8":
				return true, "PCMA/8000"
			}
		}
	}
	return true, ""
}

var q850RE = regexp.MustCompile(`(?i)Q\.850\s*;\s*cause\s*=\s*(\d+)(?:\s*;\s*text\s*=\s*"?([^";]+)"?)?`)

func parseQ850Reason(value string) (int, string) {
	match := q850RE.FindStringSubmatch(value)
	if len(match) < 2 {
		return 0, ""
	}
	cause, _ := strconv.Atoi(match[1])
	reason := ""
	if len(match) > 2 {
		reason = sanitizeSIPHeader(match[2])
	}
	if reason == "" && cause == 16 {
		reason = "NORMAL_CLEARING"
	}
	return cause, reason
}

func digestResponseForCallTest(username, realm, secret, uri, nonce string) string {
	md5hex := func(value string) string { sum := md5.Sum([]byte(value)); return hex.EncodeToString(sum[:]) }
	ha1 := md5hex(username + ":" + realm + ":" + secret)
	ha2 := md5hex("INVITE:" + uri)
	return md5hex(ha1 + ":" + nonce + ":00000001:clientnonce:auth:" + ha2)
}

func callTestAuthorization(username, realm, secret, uri, nonce string) string {
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=auth, nc=00000001, cnonce="clientnonce", algorithm=MD5`, username, realm, nonce, uri, digestResponseForCallTest(username, realm, secret, uri, nonce))
}
