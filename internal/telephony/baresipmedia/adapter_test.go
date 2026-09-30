package baresipmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

func TestFixtureBidirectionalPCMContracts(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	peerRX, peerTX, session := connectPair(t, a, rxPath, txPath)
	defer peerRX.Close()
	defer peerTX.Close()
	defer session.Close()

	rx := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcmSamples(320)}
	if err := writeSocketFrame(peerRX, rx); err != nil {
		t.Fatal(err)
	}
	gotRX, err := session.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if gotRX.Type != audiosocket.TypeSlin16 || !bytes.Equal(gotRX.Payload, rx.Payload) {
		t.Fatalf("RX contract mismatch: type=%s bytes=%d", gotRX.Type, len(gotRX.Payload))
	}

	tx := audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes / 2)}
	if err := session.WriteFrame(tx); err != nil {
		t.Fatal(err)
	}
	gotTX, err := audiosocket.DecodeReader(peerTX)
	if err != nil {
		t.Fatal(err)
	}
	if gotTX.Type != audiosocket.TypeSlin24 || len(gotTX.Payload) != txFrameBytes || !bytes.Equal(gotTX.Payload, tx.Payload) {
		t.Fatalf("TX contract mismatch: type=%s bytes=%d", gotTX.Type, len(gotTX.Payload))
	}
}

func TestTXRechunkerPreservesSamplesAcrossChunkShapes(t *testing.T) {
	tests := []struct {
		name   string
		chunks []int
		frames int
	}{
		{name: "one ptime", chunks: []int{960}, frames: 1},
		{name: "three fragments", chunks: []int{320, 320, 320}, frames: 1},
		{name: "two ptime chunks", chunks: []int{1920}, frames: 2},
		{name: "mixed 400 1200 320", chunks: []int{400, 1200, 320}, frames: 2},
		{name: "nonuniform chunks", chunks: []int{2, 318, 478, 522, 600}, frames: 2},
		{name: "maximum AudioSocket payload", chunks: []int{audiosocket.MaxPayloadSize, 896}, frames: 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 32})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			rxPath, txPath := a.SocketPaths()
			peerRX, peerTX, session := connectPair(t, a, rxPath, txPath)
			defer peerRX.Close()
			defer peerTX.Close()
			defer session.Close()

			totalBytes := 0
			for _, size := range tt.chunks {
				totalBytes += size
			}
			pcm := pcmSamples(totalBytes / 2)
			offset := 0
			for _, size := range tt.chunks {
				if err := session.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcm[offset : offset+size]}); err != nil {
					t.Fatalf("WriteFrame(%d): %v", size, err)
				}
				offset += size
			}
			if offset != len(pcm) {
				t.Fatalf("test chunks total=%d, PCM bytes=%d", offset, len(pcm))
			}

			var got []byte
			for i := 0; i < tt.frames; i++ {
				frame, err := audiosocket.DecodeReader(peerTX)
				if err != nil {
					t.Fatalf("read ptime frame %d: %v", i, err)
				}
				if frame.Type != audiosocket.TypeSlin24 || len(frame.Payload) != txFrameBytes {
					t.Fatalf("frame %d type=%s bytes=%d; want slin24/%d", i, frame.Type, len(frame.Payload), txFrameBytes)
				}
				got = append(got, frame.Payload...)
			}
			if !bytes.Equal(got, pcm) {
				t.Fatalf("rechunked PCM differs: got %d bytes, want %d", len(got), len(pcm))
			}
		})
	}
}

func TestTXRechunkerRejectsOverflowAndAdapterRecovers(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	peerRX, peerTX, first := connectPair(t, a, rxPath, txPath)
	if err := first.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes)}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("oversized queued write error=%v, want ErrBackpressure", err)
	}
	if _, err := first.ReadFrame(); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("session read error=%v, want ErrBackpressure", err)
	}
	_ = peerRX.Close()
	_ = peerTX.Close()
	waitIdle(t, a)
	assertSocketExists(t, rxPath)
	assertSocketExists(t, txPath)

	peerRX2, peerTX2, second := connectPair(t, a, rxPath, txPath)
	defer peerRX2.Close()
	defer peerTX2.Close()
	defer second.Close()
	frame := audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes / 2)}
	if err := second.WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	got, err := audiosocket.DecodeReader(peerTX2)
	if err != nil || !bytes.Equal(got.Payload, frame.Payload) {
		t.Fatalf("adapter did not recover after session overflow: got %d bytes, err=%v", len(got.Payload), err)
	}
}

func TestPartialTXFrameIsDiscardedAtSessionClose(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	_, peerTX, first := connectPair(t, a, rxPath, txPath)
	defer peerTX.Close()
	partial := pcmSamples(200)
	if err := first.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: partial}); err != nil {
		t.Fatal(err)
	}
	_ = peerTX.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
	var one [1]byte
	if _, err := peerTX.Read(one[:]); err == nil {
		t.Fatal("partial PCM was sent as a complete Baresip frame")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("read before close error=%v, want timeout without PCM", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if frame, err := first.ReadFrame(); err != nil || frame.Type != audiosocket.TypeHangup {
		t.Fatalf("explicit session close frame=%s err=%v, want hangup", frame.Type, err)
	}
	waitIdle(t, a)

	peerRX2, peerTX2, second := connectPair(t, a, rxPath, txPath)
	defer peerRX2.Close()
	defer peerTX2.Close()
	defer second.Close()
	pcm := pcmSamples(txFrameBytes / 2)
	if err := second.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcm}); err != nil {
		t.Fatal(err)
	}
	got, err := audiosocket.DecodeReader(peerTX2)
	if err != nil || !bytes.Equal(got.Payload, pcm) {
		t.Fatalf("partial CALL 1 audio leaked into CALL 2: got %d bytes, err=%v", len(got.Payload), err)
	}
}

func TestOneAdapterSupportsTwoSequentialMediaSessions(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	dir := filepath.Dir(rxPath)

	peerRX1, peerTX1, call1 := connectPair(t, a, rxPath, txPath)
	rx1 := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcmSamples(320)}
	if err := writeSocketFrame(peerRX1, rx1); err != nil {
		t.Fatal(err)
	}
	gotRX1, err := call1.ReadFrame()
	if err != nil || !bytes.Equal(gotRX1.Payload, rx1.Payload) {
		t.Fatalf("CALL 1 RX mismatch: got %d bytes, err=%v", len(gotRX1.Payload), err)
	}
	tx1 := audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes / 2)}
	if err := call1.WriteFrame(tx1); err != nil {
		t.Fatal(err)
	}
	gotTX1, err := audiosocket.DecodeReader(peerTX1)
	if err != nil || !bytes.Equal(gotTX1.Payload, tx1.Payload) {
		t.Fatalf("CALL 1 TX mismatch: got %d bytes, err=%v", len(gotTX1.Payload), err)
	}

	_ = peerRX1.Close()
	_ = peerTX1.Close()
	if frame, err := call1.ReadFrame(); err != nil || frame.Type != audiosocket.TypeHangup {
		t.Fatalf("CALL 1 disconnect frame=%s err=%v, want hangup", frame.Type, err)
	}
	waitIdle(t, a)
	assertSocketExists(t, rxPath)
	assertSocketExists(t, txPath)
	if got, err := os.Stat(dir); err != nil || got == nil {
		t.Fatalf("private listener directory did not survive CALL 1: stat=%v err=%v", got, err)
	}

	peerRX2, peerTX2, call2 := connectPair(t, a, rxPath, txPath)
	defer peerRX2.Close()
	defer peerTX2.Close()
	defer call2.Close()
	if call1.state == call2.state {
		t.Fatal("CALL 2 reused CALL 1 media session")
	}
	rx2 := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcmSamplesWithSeed(320, 700)}
	if err := writeSocketFrame(peerRX2, rx2); err != nil {
		t.Fatal(err)
	}
	gotRX2, err := call2.ReadFrame()
	if err != nil || !bytes.Equal(gotRX2.Payload, rx2.Payload) {
		t.Fatalf("CALL 2 RX mismatch or stale CALL 1 audio: got %d bytes, err=%v", len(gotRX2.Payload), err)
	}
	tx2 := audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamplesWithSeed(txFrameBytes/2, 900)}
	if err := call2.WriteFrame(tx2); err != nil {
		t.Fatal(err)
	}
	gotTX2, err := audiosocket.DecodeReader(peerTX2)
	if err != nil || !bytes.Equal(gotTX2.Payload, tx2.Payload) {
		t.Fatalf("CALL 2 TX mismatch or stale CALL 1 audio: got %d bytes, err=%v", len(gotTX2.Payload), err)
	}

	if gotRXPath, gotTXPath := a.SocketPaths(); gotRXPath != rxPath || gotTXPath != txPath {
		t.Fatalf("socket paths changed between calls: (%s,%s) -> (%s,%s)", rxPath, txPath, gotRXPath, gotTXPath)
	}
}

func TestSecondMediaPairIsRejectedWhileCallActive(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	peerRX, peerTX, session := connectPair(t, a, rxPath, txPath)
	defer peerRX.Close()
	defer peerTX.Close()
	defer session.Close()

	duplicateRX, err := dialUnix(t, rxPath)
	if err != nil {
		t.Fatal(err)
	}
	assertRejectedConnection(t, duplicateRX)
	duplicateTX, err := dialUnix(t, txPath)
	if err != nil {
		t.Fatal(err)
	}
	assertRejectedConnection(t, duplicateTX)

	rx := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcmSamplesWithSeed(320, 400)}
	if err := writeSocketFrame(peerRX, rx); err != nil {
		t.Fatal(err)
	}
	got, err := session.ReadFrame()
	if err != nil || !bytes.Equal(got.Payload, rx.Payload) {
		t.Fatalf("extra pair affected active call: got %d bytes, err=%v", len(got.Payload), err)
	}
}

func TestRejectsWrongTypeOddPCMAndOversizeChunk(t *testing.T) {
	for _, frame := range []audiosocket.Frame{
		{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2}},
		{Type: audiosocket.TypeSlin24, Payload: []byte{1}},
		{Type: audiosocket.TypeSlin24},
		{Type: audiosocket.TypeSlin24, Payload: make([]byte, audiosocket.MaxPayloadSize+2)},
	} {
		if err := validateVariableFrame(frame, audiosocket.TypeSlin24); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("validateVariableFrame(%s, %d) error = %v, want ErrInvalidFormat", frame.Type, len(frame.Payload), err)
		}
	}
}

func TestRXQueueOverflowEndsOnlyCurrentSession(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	peerRX, peerTX, session := connectPair(t, a, rxPath, txPath)
	frame := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcmSamples(320)}
	for range 16 {
		if err := writeSocketFrame(peerRX, frame); err != nil {
			select {
			case <-session.state.done:
				break
			default:
				t.Fatal(err)
			}
			break
		}
	}
	select {
	case <-session.state.done:
	case <-time.After(time.Second):
		t.Fatal("RX queue did not fail closed after bounded overflow")
	}
	if err := session.state.sessionError(); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("session error=%v, want ErrBackpressure", err)
	}
	_ = peerRX.Close()
	_ = peerTX.Close()
	waitIdle(t, a)
	assertSocketExists(t, rxPath)
	assertSocketExists(t, txPath)
}

func TestCancellationClosesListenersAndCleansPrivatePaths(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, Config{ParentDir: parent})
	if err != nil {
		t.Fatal(err)
	}
	dir := a.dir
	rxPath, txPath := a.SocketPaths()
	if err := cancelAndWait(a, cancel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rxPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("RX socket path remains after cancellation: %v", err)
	}
	if _, err := os.Stat(txPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TX socket path remains after cancellation: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private directory remains after cancellation: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter directory still exists: %v", err)
	}
}

func TestSocketPermissions(t *testing.T) {
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rx, tx := a.SocketPaths()
	dir, err := os.Stat(filepath.Dir(rx))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Mode().Perm(); got != 0700 {
		t.Fatalf("private directory mode=%#o, want 0700", got)
	}
	for _, path := range []string{rx, tx} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("socket %s mode=%#o, want 0600", path, got)
		}
	}
}

func connectPair(t *testing.T, a *Adapter, rxPath, txPath string) (*net.UnixConn, *net.UnixConn, *Session) {
	t.Helper()
	peerRX, err := dialUnix(t, rxPath)
	if err != nil {
		t.Fatal(err)
	}
	peerTX, err := dialUnix(t, txPath)
	if err != nil {
		_ = peerRX.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := a.WaitSession(ctx)
	if err != nil {
		_ = peerRX.Close()
		_ = peerTX.Close()
		t.Fatal(err)
	}
	return peerRX, peerTX, session
}

func dialUnix(t *testing.T, path string) (*net.UnixConn, error) {
	t.Helper()
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
}

func writeSocketFrame(conn net.Conn, frame audiosocket.Frame) error {
	wire, err := audiosocket.Encode(frame)
	if err != nil {
		return err
	}
	return writeFull(conn, wire)
}

func pcmSamples(count int) []byte { return pcmSamplesWithSeed(count, 1) }

func pcmSamplesWithSeed(count, seed int) []byte {
	pcm := make([]byte, count*2)
	for i := 0; i < count; i++ {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(i*37+seed)))
	}
	return pcm
}

func assertRejectedConnection(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("extra media connection read error=%v, want EOF/rejection", err)
	}
}

func waitIdle(t *testing.T, a *Adapter) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		idle := a.active == nil
		a.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("media session did not detach")
}

func assertSocketExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stable socket %s missing: %v", path, err)
	}
}

func cancelAndWait(a *Adapter, cancel context.CancelFunc) error {
	cancel()
	select {
	case <-a.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("context cancellation did not close adapter")
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
