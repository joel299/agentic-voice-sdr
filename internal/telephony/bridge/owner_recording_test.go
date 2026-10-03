package bridge

import (
	"context"
	"encoding/binary"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerRecordingOptInPrivatePCMAndCleanup(t *testing.T) {
	dir := t.TempDir()
	r, e := NewOwnerRecording(context.Background(), OwnerRecordingConfig{Enabled: true, OwnerTest: true, Directory: dir, BufferFrames: 2})
	if e != nil {
		t.Fatal(e)
	}
	r.Capture(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: []byte{1, 0, 2, 0}})
	r.Close()
	p := r.Paths()[0]
	info, e := os.Stat(p)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file %v %v", info, e)
	}
	b, _ := os.ReadFile(p)
	if string(b[:4]) != "RIFF" || binary.LittleEndian.Uint32(b[24:28]) != 16000 || binary.LittleEndian.Uint32(b[40:44]) != 4 {
		t.Fatal("invalid recording")
	}
}
func TestRecordingDisabledAndOwnerGate(t *testing.T) {
	r, e := NewOwnerRecording(context.Background(), OwnerRecordingConfig{})
	if e != nil || r != nil {
		t.Fatal("enabled by default")
	}
	if _, e = NewOwnerRecording(context.Background(), OwnerRecordingConfig{Enabled: true, Directory: t.TempDir()}); e == nil {
		t.Fatal("owner gate bypass")
	}
}

func TestOwnerRecordingCancellationAndRetention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	r, e := NewOwnerRecording(ctx, OwnerRecordingConfig{Enabled: true, OwnerTest: true, Directory: dir, BufferFrames: 1})
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	r.Close()
	r.Capture(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: []byte{1, 0}})
	old := filepath.Dir(r.Paths()[0])
	past := time.Now().Add(-25 * time.Hour)
	if e = os.Chtimes(old, past, past); e != nil {
		t.Fatal(e)
	}
	untouched := filepath.Join(dir, "unrelated")
	if e = os.Mkdir(untouched, 0700); e != nil {
		t.Fatal(e)
	}
	if e = ExpireOwnerRecordings(dir, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("expired recording retained")
	}
	if _, e = os.Stat(untouched); e != nil {
		t.Fatal("unrelated path removed")
	}
}
func TestOwnerRecordingPressureNeverWaitsForDisk(t *testing.T) {
	r := &OwnerRecording{queue: make(chan audiosocket.Frame, 1)}
	f := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: []byte{1, 0}}
	r.Capture(f)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			r.Capture(f)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recording blocked hot path")
	}
	if r.dropped.Load() != 1000 || len(r.queue) != 1 {
		t.Fatal("recording queue not bounded")
	}
}
