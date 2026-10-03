package baresipctrl

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// MediaSourceStats is a safe snapshot of the module's process counters. Delta
// counters are usable only across a known single-call interval. MaxBufferedBytes
// is a process high-water gauge and must never be presented as a delta.
type MediaSourceStats struct {
	FramesReceived   uint64 `json:"c_frames_received"`
	FramesEmitted    uint64 `json:"c_frames_emitted"`
	SilenceFrames    uint64 `json:"c_silence_frames"`
	SocketErrors     uint64 `json:"c_socket_errors"`
	ProtocolErrors   uint64 `json:"c_protocol_errors"`
	MaxBufferedBytes uint64 `json:"c_process_max_buffered_bytes"`
	LastErrorClass   string `json:"c_process_last_error_class"`
}

func (c *Client) MediaSourceStats(ctx context.Context) (MediaSourceStats, error) {
	r, err := c.Do(ctx, "gru151_media_stats", "")
	if err != nil || !r.OK {
		return MediaSourceStats{}, errors.New("media source stats unavailable")
	}
	return parseMediaSourceStats(r.Data)
}
func parseMediaSourceStats(data string) (MediaSourceStats, error) {
	var s MediaSourceStats
	values := map[string]*uint64{"source_frames_received": &s.FramesReceived, "source_frames_emitted": &s.FramesEmitted, "source_silence_frames": &s.SilenceFrames, "source_socket_errors": &s.SocketErrors, "source_protocol_errors": &s.ProtocolErrors, "source_max_buffered_bytes": &s.MaxBufferedBytes}
	seen := make(map[string]bool)
	if len(data) > 2048 {
		return s, errors.New("media source stats invalid")
	}
	for _, field := range strings.Fields(data) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || seen[key] {
			return MediaSourceStats{}, errors.New("media source stats invalid")
		}
		seen[key] = true
		if ptr, ok := values[key]; ok {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return MediaSourceStats{}, errors.New("media source stats invalid")
			}
			*ptr = n
			continue
		}
		if key != "source_last_error_class" {
			return MediaSourceStats{}, errors.New("media source stats invalid")
		}
		switch value {
		case "none", "socket_io", "frame_protocol", "clock", "peer_closed", "allocation", "unknown":
			s.LastErrorClass = value
		default:
			return MediaSourceStats{}, errors.New("media source stats invalid")
		}
	}
	if len(seen) != 7 {
		return MediaSourceStats{}, errors.New("media source stats incomplete")
	}
	return s, nil
}

// Delta rejects resets instead of wrapping counts or mixing Baresip processes.
func (s MediaSourceStats) Delta(before MediaSourceStats) (MediaSourceStats, bool) {
	if s.FramesReceived < before.FramesReceived || s.FramesEmitted < before.FramesEmitted || s.SilenceFrames < before.SilenceFrames || s.SocketErrors < before.SocketErrors || s.ProtocolErrors < before.ProtocolErrors {
		return MediaSourceStats{}, false
	}
	s.FramesReceived -= before.FramesReceived
	s.FramesEmitted -= before.FramesEmitted
	s.SilenceFrames -= before.SilenceFrames
	s.SocketErrors -= before.SocketErrors
	s.ProtocolErrors -= before.ProtocolErrors
	return s, true
}
