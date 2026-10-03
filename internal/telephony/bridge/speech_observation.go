package bridge

import (
	"encoding/binary"
	"sync"
	"time"
)

// This is a diagnostic estimate, not semantic VAD and not a provider speech-end
// event. It never suppresses PCM, finalizes turns or interrupts a response.
type speechObservation struct {
	mu         sync.Mutex
	lastVoiced time.Time
}

func (s *speechObservation) Note(p []byte, at time.Time) {
	if len(p) < 2 {
		return
	}
	var sum int64
	for i := 0; i+1 < len(p); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(p[i:])))
		if v < 0 {
			v = -v
		}
		sum += int64(v)
	}
	if sum/int64(len(p)/2) < 500 {
		return
	}
	s.mu.Lock()
	s.lastVoiced = at
	s.mu.Unlock()
}
func (s *speechObservation) EndEstimate() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastVoiced
}
