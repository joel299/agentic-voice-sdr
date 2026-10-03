package geminilive

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
)

// HybridInput preserves server speech-start detection. A conservative local
// energy detector flushes after silence; it is an opt-in calibrated strategy,
// not semantic VAD. The default continues to use server automatic VAD only.
type hybridInput struct {
	InputTranscriberSession
	mu        sync.Mutex
	threshold int
	silenceMS int
	active    bool
	ended     bool
	quietMS   int
}

func WithHybridVAD(s InputTranscriberSession, silenceMS int) InputTranscriberSession {
	if silenceMS < 500 {
		silenceMS = 1200
	}
	return &hybridInput{InputTranscriberSession: s, threshold: 500, silenceMS: silenceMS}
}
func (h *hybridInput) SendAudio(ctx context.Context, p []byte) error {
	if len(p) == 0 || len(p)%2 != 0 {
		return errors.New("hybrid VAD requires PCM16 samples")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var total int64
	for i := 0; i+1 < len(p); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(p[i:])))
		if v < 0 {
			v = -v
		}
		total += int64(v)
	}
	voiced := len(p) > 0 && total/int64(len(p)/2) >= int64(h.threshold)
	if voiced {
		h.active = true
		h.ended = false
		h.quietMS = 0
	} else if h.active {
		h.quietMS += len(p) * 1000 / (InputSampleRate * 2)
	}
	if h.ended && !voiced {
		return nil
	}
	if e := h.InputTranscriberSession.SendAudio(ctx, p); e != nil {
		return e
	}
	if h.active && h.quietMS >= h.silenceMS {
		if e := h.InputTranscriberSession.EndAudio(ctx); e != nil {
			return e
		}
		h.active = false
		h.ended = true
	}
	return nil
}

func (*hybridInput) ownsLeadTurnIdentity() {}
func (h *hybridInput) attachLeadTurnSequencer(s *LeadTurnSequencer) {
	if o, ok := h.InputTranscriberSession.(leadTurnIdentityOwner); ok {
		o.attachLeadTurnSequencer(s)
	}
}
