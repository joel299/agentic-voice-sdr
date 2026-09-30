package baresipctrl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

var activeCallsHeader = regexp.MustCompile(`(?i)^\s*---\s*(?:List of )?active calls\s*\(\s*(\d+)\s*\)\s*:?\s*---\s*$`)
var callURI = regexp.MustCompile(`(?i)sips?:[^\s,]+`)
var callIDField = regexp.MustCompile(`(?i)(?:id|call[_ -]?id)\s*[=:]\s*([^\s,]+)`)

// ParseActiveCalls converts Baresip v1.1 listcalls text to provider-neutral
// inventory. Unknown/nonempty formats fail closed so the caller cannot dial
// based on an unrecognized response.
func ParseActiveCalls(output string) ([]control.ActiveCall, error) {
	clean := strings.TrimSpace(ansiEscape.ReplaceAllString(output, ""))
	if clean == "" {
		return nil, fmt.Errorf("empty Baresip call inventory")
	}
	lines := strings.Split(clean, "\n")
	var headers []int
	var counts []int
	noActiveMarker := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "--- No active calls ---") {
			noActiveMarker = true
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "active calls") {
			match := activeCallsHeader.FindStringSubmatch(trimmed)
			if len(match) != 2 {
				return nil, fmt.Errorf("invalid Baresip call inventory header")
			}
			count, err := strconv.Atoi(match[1])
			if err != nil {
				return nil, fmt.Errorf("invalid Baresip active call count")
			}
			headers = append(headers, i)
			counts = append(counts, count)
		}
	}
	if len(headers) == 0 && noActiveMarker {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.EqualFold(trimmed, "--- No active calls ---") || strings.HasPrefix(strings.ToLower(trimmed), "user-agent:") {
				continue
			}
			return nil, fmt.Errorf("unrecognized Baresip call inventory")
		}
		return []control.ActiveCall{}, nil
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("unrecognized Baresip call inventory")
	}
	for _, line := range lines[:headers[0]] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "user-agent:") || strings.EqualFold(trimmed, "--- No active calls ---") {
			continue
		}
		return nil, fmt.Errorf("unrecognized Baresip call inventory preamble")
	}

	total := 0
	for _, count := range counts {
		if count > len(lines)-total {
			return nil, fmt.Errorf("Baresip active call count exceeds response size")
		}
		total += count
	}
	calls := make([]control.ActiveCall, 0, total)
	for section, headerIndex := range headers {
		expected := counts[section]
		end := len(lines)
		if section+1 < len(headers) {
			end = headers[section+1]
		}
		sectionCalls := make([]control.ActiveCall, 0, expected)
		for _, line := range lines[headerIndex+1 : end] {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(line), "user-agent:") {
				break
			}
			if line == "" || strings.EqualFold(line, "--- No active calls ---") {
				continue
			}
			if strings.HasPrefix(line, "---") {
				return nil, fmt.Errorf("unrecognized Baresip call inventory section")
			}
			if len(sectionCalls) >= expected {
				return nil, fmt.Errorf("Baresip call rows exceed reported active count")
			}
			uri := callURI.FindString(line)
			if uri == "" {
				sectionCalls = append(sectionCalls, control.ActiveCall{})
				continue
			}
			call := control.ActiveCall{PeerURI: strings.TrimRight(uri, ".,;)")}
			if id := callIDField.FindStringSubmatch(line); len(id) == 2 {
				call.ProviderCallID = strings.TrimRight(id[1], ".,;)")
			}
			lower := strings.ToLower(line)
			switch {
			case strings.Contains(lower, "established"), strings.Contains(lower, "connected"):
				call.State = control.CallStateConnected
			case strings.Contains(lower, "ringing"), strings.Contains(lower, "progress"):
				call.State = control.CallStateRinging
			default:
				call.State = control.CallStateOutgoing
			}
			sectionCalls = append(sectionCalls, call)
		}
		calls = append(calls, sectionCalls...)
		// Preserve every provider-reported call even when a row is malformed.
		for len(sectionCalls) < expected {
			calls = append(calls, control.ActiveCall{})
			sectionCalls = append(sectionCalls, control.ActiveCall{})
		}
	}
	return calls, nil
}
