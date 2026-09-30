package baresipctrl

import (
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

func TestParseActiveCalls(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
		state control.CallState
		peer  string
	}{
		{name: "no active calls", input: "--- No active calls ---", want: 0},
		{name: "zero header", input: "  --- Active calls (0) ---  ", want: 0},
		{name: "one standard list", input: "--- List of active calls (1): ---\n  0:00:05 ESTABLISHED sip:alice@example.net  ", want: 1, state: control.CallStateConnected, peer: "sip:alice@example.net"},
		{name: "multiple ansi rows", input: "\x1b[32m--- Active calls (2) ---\x1b[0m\n0:00:01 CALLING sip:+15550000001@example.net id=first\n  0:00:02 RINGING sip:+15550000002@example.net call_id=second", want: 2, state: control.CallStateOutgoing, peer: "sip:+15550000001@example.net"},
		{name: "unknown row retained", input: "--- Active calls (2) ---\n0:00:01 CALLING sip:+15550000001@example.net", want: 2},
		{name: "agent zero then one", input: "User-Agent: account-a\n--- Active calls (0) ---\nUser-Agent: sip:account-b@example.net\n--- Active calls (1) ---\n0:00:10 ESTABLISHED sip:+5567981340687@example.net", want: 1},
		{name: "agent one then zero", input: "User-Agent: account-a\n--- Active calls (1) ---\n0:00:10 ESTABLISHED sip:+5567981340687@example.net\nUser-Agent: account-b\n--- Active calls (0) ---", want: 1},
		{name: "aggregate one plus two", input: "User-Agent: account-a\n--- Active calls (1) ---\n0:00:01 CALLING sip:+15550000001@example.net\nUser-Agent: account-b\n--- Active calls (2) ---\n0:00:02 CALLING sip:+15550000002@example.net\n0:00:03 RINGING sip:+15550000003@example.net", want: 3},
		{name: "all user agents zero", input: "User-Agent: account-a\n--- Active calls (0) ---\nUser-Agent: account-b\n--- Active calls (0) ---", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, err := ParseActiveCalls(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != tt.want {
				t.Fatalf("calls=%d, want %d: %+v", len(calls), tt.want, calls)
			}
			if tt.peer != "" && (calls[0].PeerURI != tt.peer || calls[0].State != tt.state) {
				t.Fatalf("call=%+v", calls[0])
			}
			if tt.name == "multiple ansi rows" && (calls[0].ProviderCallID != "first" || calls[1].ProviderCallID != "second") {
				t.Fatalf("IDs not parsed: %+v", calls)
			}
			if tt.name == "unknown row retained" && (calls[1].ProviderCallID != "" || calls[1].PeerURI != "") {
				t.Fatalf("expected opaque slot preserving provider count: %+v", calls)
			}
		})
	}
}

func TestParseActiveCallsRejectsUnknownOutput(t *testing.T) {
	if _, err := ParseActiveCalls("success"); err == nil {
		t.Fatal("unknown output must fail closed")
	}
	if _, err := ParseActiveCalls("--- Active calls (2) ---"); err == nil {
		t.Fatal("reported inventory count larger than response shape must fail closed")
	}
}
