package baresipctrl

import (
	"strings"
	"testing"
)

func TestMediaSourceStatsSafeParsingAndCallIntervalDelta(t *testing.T) {
	raw := "source_frames_received=9 source_frames_emitted=12 source_silence_frames=3 source_protocol_errors=0 source_socket_errors=0 source_max_buffered_bytes=963 source_last_error_class=none"
	s, err := parseMediaSourceStats(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.Delta(MediaSourceStats{FramesReceived: 4, FramesEmitted: 5, SilenceFrames: 1})
	if !ok || d.FramesReceived != 5 || d.FramesEmitted != 7 || d.SilenceFrames != 2 || d.MaxBufferedBytes != 963 {
		t.Fatalf("delta=%+v", d)
	}
	if _, ok = s.Delta(MediaSourceStats{FramesReceived: 10}); ok {
		t.Fatal("reset accepted")
	}
	for _, bad := range []string{raw + " password=secret", strings.Replace(raw, "=none", "=SECRET", 1), raw + " source_frames_received=0", strings.Replace(raw, "received=9", "received=-1", 1), "source_frames_received=1"} {
		if _, err := parseMediaSourceStats(bad); err == nil {
			t.Fatal("unsafe/incomplete stats accepted")
		}
	}
}
