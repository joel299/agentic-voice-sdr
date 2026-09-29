package main

import "testing"

func TestClassifyCaptureErrorRedactsToSafeClass(t *testing.T) {
	cases := map[string]string{"permission denied opening capture": "permission_denied", "pcap file is truncated": "truncated_pcap", "invalid pcap header": "invalid_pcap", "context deadline exceeded": "unknown"}
	for input, want := range cases {
		if got := classifyCaptureError(input); got != want {
			t.Fatalf("%q: got %q want %q", input, got, want)
		}
	}
}
