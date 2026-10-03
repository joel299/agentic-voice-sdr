package baresipmedia

import (
	"context"
	"encoding/binary"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"os"
	"testing"
	"time"
)

func TestMediaTimingAckIsCorrelatedAndFragmented(t *testing.T) {
	a, e := New(context.Background(), Config{ParentDir: shortTempDir(t)})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	rx, tx := a.SocketPaths()
	peerRX, peerTX, s := connectPair(t, a, rx, tx)
	defer peerRX.Close()
	defer peerTX.Close()
	defer s.Close()
	c := telemetry.NewTurnCollector(1)
	ctx, tr := c.Begin(context.Background(), "lead-000001", time.Now())
	if e = s.WriteFrameContext(ctx, audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(480)}); e != nil {
		t.Fatal(e)
	}
	if _, e = audiosocket.DecodeReader(peerTX); e != nil {
		t.Fatal(e)
	}
	var ack [32]byte
	copy(ack[:], "GTIM")
	binary.BigEndian.PutUint64(ack[4:], 1)
	binary.BigEndian.PutUint64(ack[12:], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(ack[20:], uint64(time.Now().UnixNano()))
	peerTX.Write(ack[:3])
	peerTX.Write(ack[3:])
	deadline := time.Now().Add(time.Second)
	for tr.Snapshot().Times["c_source_first_real_frame_at"].IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snap := tr.Snapshot()
	if snap.Times["go_first_pcm_write_at"].IsZero() || snap.Times["c_source_first_real_frame_at"].IsZero() {
		t.Fatal("wire clocks absent")
	}
}

func TestRuntimeBufferBudgetIsExplicitAndInvalidConfigLeaksNoDirectory(t *testing.T) {
	parent := shortTempDir(t)
	for _, cfg := range []Config{{ParentDir: parent, RXBufferFrames: 257}, {ParentDir: parent, TXSocketBufferBytes: 1}} {
		if a, e := New(context.Background(), cfg); e == nil {
			a.Close()
			t.Fatal("invalid budget accepted")
		}
	}
	entries, e := os.ReadDir(parent)
	if e != nil || len(entries) != 0 {
		t.Fatal("invalid config leaked directory")
	}
	a, e := New(context.Background(), Config{ParentDir: parent, BufferFrames: 2, RXBufferFrames: 4, TXSocketBufferBytes: 1024})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if a.rxFrames != 4 || a.txFrames != 2 || a.txSocketBuffer != 1024 {
		t.Fatal("runtime buffer budget ignored")
	}
}
