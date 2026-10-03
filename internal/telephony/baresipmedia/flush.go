package baresipmedia

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// DiscardPendingAudio stops admission/writes while the C source discards old
// IPC bytes at its next 20 ms tick. An explicit ACK is required before resuming.
// It neither closes the media sockets nor sends a SIP hangup.
func (s *Session) DiscardPendingAudio(ctx context.Context) error {
	if s == nil || s.state == nil {
		return ErrSessionClosed
	}
	st := s.state
	st.txMu.Lock()
	defer st.txMu.Unlock()
	st.wireMu.Lock()
	defer st.wireMu.Unlock()
	clear(st.txPartial[:])
	st.txUsed = 0
	for len(st.txQueue) > 0 {
		f := <-st.txQueue
		clear(f.Payload)
	}
	st.timingMu.Lock()
	st.timingEpoch++
	epoch := st.timingEpoch
	clear(st.timingPending)
	st.wireSequence = 0
	st.timingMu.Unlock()
	peer, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: s.adapter.txPath + ".flush", Net: "unixgram"})
	if err != nil {
		return errors.New("C media flush control unavailable")
	}
	defer peer.Close()
	_ = peer.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(epoch))
	if _, err = peer.Write(b[:]); err != nil {
		return errors.New("C media flush request failed")
	}
	timeout := time.NewTimer(250 * time.Millisecond)
	defer timeout.Stop()
	for {
		select {
		case ack := <-st.flushDone:
			if ack == uint64(epoch) {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-st.done:
			return ErrSessionClosed
		case <-timeout.C:
			return errors.New("C media flush acknowledgement timeout")
		}
	}
}
