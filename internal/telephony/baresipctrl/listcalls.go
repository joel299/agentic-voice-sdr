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
	for _, line := range strings.Split(clean, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "--- No active calls ---") {
			return []control.ActiveCall{}, nil
		}
	}

	lines := strings.Split(clean, "\n")
	headerIndex, expected := -1, -1
	for i, line := range lines {
		match := activeCallsHeader.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 2 {
			headerIndex = i
			expected, _ = strconv.Atoi(match[1])
			break
		}
	}
	if headerIndex < 0 {
		return nil, fmt.Errorf("unrecognized Baresip call inventory")
	}
	if expected == 0 {
		return []control.ActiveCall{}, nil
	}

	calls := make([]control.ActiveCall, 0, expected)
	for _, line := range lines[headerIndex+1:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") {
			continue
		}
		uri := callURI.FindString(line)
		if uri == "" {
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
		calls = append(calls, call)
	}
	// If Baresip reports active calls without parseable peers, retain opaque
	// entries. Any nonempty inventory must block a new dial.
	for len(calls) < expected {
		calls = append(calls, control.ActiveCall{})
	}
	return calls, nil
}
