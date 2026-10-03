package bridge

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Diagnostic PCM is private and explicitly owner opted-in. A bounded queue
// drops rather than blocking audio. Separate streams are not a mixed call replay.
type OwnerRecordingConfig struct {
	Enabled, OwnerTest bool
	Directory          string
	BufferFrames       int
}
type OwnerRecording struct {
	mu      sync.Mutex
	closed  bool
	queue   chan audiosocket.Frame
	stop    chan struct{}
	done    chan struct{}
	files   [2]*os.File
	paths   [2]string
	bytes   [2]uint32
	dropped atomic.Uint64
	failed  atomic.Bool
}

func NewOwnerRecording(ctx context.Context, c OwnerRecordingConfig) (*OwnerRecording, error) {
	if !c.Enabled {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("recording context required")
	}
	if !c.OwnerTest || c.Directory == "" {
		return nil, errors.New("owner recording requires explicit owner test configuration")
	}
	if c.BufferFrames <= 0 {
		c.BufferFrames = 32
	}
	if c.BufferFrames > 256 {
		return nil, errors.New("recording capacity exceeds bound")
	}
	if e := os.MkdirAll(c.Directory, 0700); e != nil {
		return nil, errors.New("recording directory unavailable")
	}
	dir, e := os.MkdirTemp(c.Directory, "owner-test-")
	if e != nil {
		return nil, e
	}
	r := &OwnerRecording{queue: make(chan audiosocket.Frame, c.BufferFrames), stop: make(chan struct{}), done: make(chan struct{})}
	for i, n := range []string{"lead.wav", "agent.wav"} {
		p := filepath.Join(dir, n)
		f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			for _, x := range r.files {
				if x != nil {
					x.Close()
				}
			}
			os.RemoveAll(dir)
			return nil, errors.New("recording file unavailable")
		}
		r.files[i] = f
		r.paths[i] = p
		if _, e = f.Write(make([]byte, 44)); e != nil {
			for _, x := range r.files {
				if x != nil {
					_ = x.Close()
				}
			}
			_ = os.RemoveAll(dir)
			return nil, errors.New("recording header unavailable")
		}
	}
	go r.run(ctx)
	return r, nil
}
func (r *OwnerRecording) Capture(f audiosocket.Frame) {
	if r == nil || (f.Type != audiosocket.TypeSlin16 && f.Type != audiosocket.TypeSlin24) || len(f.Payload) == 0 || len(f.Payload) > audiosocket.MaxPayloadSize || len(f.Payload)%2 != 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	select {
	case r.queue <- audiosocket.Frame{Type: f.Type, Payload: append([]byte(nil), f.Payload...)}:
	default:
		r.dropped.Add(1)
	}
}
func (r *OwnerRecording) run(ctx context.Context) {
	defer close(r.done)
	defer r.finalize()
	write := func(f audiosocket.Frame) {
		i := 0
		rate := uint32(16000)
		if f.Type == audiosocket.TypeSlin24 {
			i = 1
			rate = 24000
		}
		if r.bytes[i]+uint32(len(f.Payload)) > rate*2*600 {
			r.dropped.Add(1)
			return
		}
		n, e := r.files[i].Write(f.Payload)
		r.bytes[i] += uint32(n)
		if e != nil || n != len(f.Payload) {
			r.failed.Store(true)
		}
		clear(f.Payload)
	}
	for {
		select {
		case f := <-r.queue:
			write(f)
		case <-ctx.Done():
			r.mu.Lock()
			if !r.closed {
				r.closed = true
				close(r.stop)
			}
			r.mu.Unlock()
			for len(r.queue) > 0 {
				write(<-r.queue)
			}
			return
		case <-r.stop:
			for len(r.queue) > 0 {
				write(<-r.queue)
			}
			return
		}
	}
}
func (r *OwnerRecording) finalize() {
	for i, f := range r.files {
		rate := uint32(16000)
		if i == 1 {
			rate = 24000
		}
		b := make([]byte, 44)
		copy(b, "RIFF")
		binary.LittleEndian.PutUint32(b[4:8], 36+r.bytes[i])
		copy(b[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(b[16:20], 16)
		binary.LittleEndian.PutUint16(b[20:22], 1)
		binary.LittleEndian.PutUint16(b[22:24], 1)
		binary.LittleEndian.PutUint32(b[24:28], rate)
		binary.LittleEndian.PutUint32(b[28:32], rate*2)
		binary.LittleEndian.PutUint16(b[32:34], 2)
		binary.LittleEndian.PutUint16(b[34:36], 16)
		copy(b[36:], "data")
		binary.LittleEndian.PutUint32(b[40:44], r.bytes[i])
		if _, e := f.Seek(0, 0); e != nil {
			r.failed.Store(true)
		}
		if _, e := f.Write(b); e != nil {
			r.failed.Store(true)
		}
		if e := f.Close(); e != nil {
			r.failed.Store(true)
		}
	}
}
func (r *OwnerRecording) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stop)
	}
	r.mu.Unlock()
	<-r.done
}
func (r *OwnerRecording) Paths() []string {
	if r == nil {
		return nil
	}
	return []string{r.paths[0], r.paths[1]}
}
func (r *OwnerRecording) Complete() bool {
	return r != nil && r.dropped.Load() == 0 && !r.failed.Load()
}

// ExpireOwnerRecordings removes only this diagnostic's private subdirectories.
// Call outside realtime; callers must keep retention at most 24 hours.
func ExpireOwnerRecordings(dir string, now time.Time) error {
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, x := range entries {
		if !x.IsDir() || len(x.Name()) < 11 || x.Name()[:11] != "owner-test-" {
			continue
		}
		info, e := x.Info()
		if e != nil {
			return e
		}
		if now.Sub(info.ModTime()) > 24*time.Hour {
			if e = os.RemoveAll(filepath.Join(dir, x.Name())); e != nil {
				return e
			}
		}
	}
	return nil
}
