package baresipmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

func TestTXQueueAppliesBoundedBackpressureWithoutEndingSession(t *testing.T) {
	state := &mediaSession{txQueue: make(chan txFrame, 1), done: make(chan struct{})}
	pcm := pcmSamples(txFrameBytes)
	writeDone := make(chan error, 1)
	go func() { writeDone <- state.enqueuePCM(context.Background(), pcm) }()

	deadline := time.Now().Add(time.Second)
	for len(state.txQueue) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(state.txQueue) != 1 {
		t.Fatal("producer did not fill the bounded queue")
	}
	select {
	case err := <-writeDone:
		t.Fatalf("writer returned before bounded queue had capacity: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	first := <-state.txQueue
	if !bytes.Equal(first.Payload, pcm[:txFrameBytes]) {
		t.Fatal("first queued frame did not preserve PCM order")
	}
	second := <-state.txQueue
	if !bytes.Equal(second.Payload, pcm[txFrameBytes:]) {
		t.Fatal("second queued frame did not preserve PCM order")
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("WriteFrame returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not resume after queue capacity became available")
	}
}

func TestTXQueueBackpressureStopsOnSessionCancellation(t *testing.T) {
	state := &mediaSession{txQueue: make(chan txFrame, 1), done: make(chan struct{})}
	state.txQueue <- txFrame{Frame: audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes)}}
	writeDone := make(chan error, 1)
	go func() { writeDone <- state.enqueuePCM(context.Background(), pcmSamples(2*txFrameBytes)) }()

	select {
	case err := <-writeDone:
		t.Fatalf("writer did not wait for bounded queue capacity: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(state.done)
	select {
	case err := <-writeDone:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("WriteFrame error=%v, want ErrSessionClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not stop after session cancellation")
	}
}

func TestFiveSecondGeminiTXBurstPreservesOrderAndQueueBounds(t *testing.T) {
	const frames = 250 // 5s of 20ms, mono 24kHz, S16LE audio.
	const queueCapacity = 8
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := New(ctx, Config{ParentDir: shortTempDir(t), BufferFrames: queueCapacity})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	peerRX, peerTX, session := connectPair(t, a, rxPath, txPath)
	defer peerRX.Close()
	defer peerTX.Close()
	defer session.Close()
	want := make([]byte, frames*txFrameBytes)
	for frame := 0; frame < frames; frame++ {
		for i := 0; i < txFrameBytes; i++ {
			want[frame*txFrameBytes+i] = byte(frame)
		}
	}
	consumerErr := make(chan error, 1)
	go func() {
		stream := audiosocket.NewStream(peerTX, nil)
		for frame := 0; frame < frames; frame++ {
			got, err := stream.ReadFrame()
			if err != nil {
				consumerErr <- err
				return
			}
			if got.Type != audiosocket.TypeSlin24 || !bytes.Equal(got.Payload, want[frame*txFrameBytes:(frame+1)*txFrameBytes]) {
				consumerErr <- errors.New("TX burst frame order or PCM content changed")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		consumerErr <- nil
	}()
	// 16 native PCM chunks arrive in immediate bursts. The fixed-size
	// queue forces the producer to wait while the socket consumer plays in order.
	for offset := 0; offset < len(want); offset += 16 * txFrameBytes {
		end := offset + 16*txFrameBytes
		if end > len(want) {
			end = len(want)
		}
		if err := session.WriteFrameContext(ctx, audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: want[offset:end]}); err != nil {
			t.Fatalf("send PCM burst at %d: %v", offset, err)
		}
	}
	if err := <-consumerErr; err != nil {
		t.Fatal(err)
	}
	if session.IsClosed() {
		t.Fatal("TX burst ended the media session")
	}
	metrics := session.Metrics()
	if metrics.TXQueueHighWater > queueCapacity {
		t.Fatalf("TX high-water=%d exceeds configured capacity %d", metrics.TXQueueHighWater, queueCapacity)
	}
	if metrics.TXWaitCount == 0 || metrics.TXWaitDurationMS == 0 {
		t.Fatalf("TX backpressure metrics did not record waits: %+v", metrics)
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

func TestRXQueueOverflowDropsStaleFrameWithoutEndingSession(t *testing.T) {
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
			t.Fatal(err)
		}
	}
	select {
	case <-session.state.done:
		t.Fatal("RX overflow terminated the media session")
	case <-time.After(20 * time.Millisecond):
	}
	metrics := session.Metrics()
	if metrics.RXFramesDropped == 0 {
		t.Fatal("RX overflow did not increment dropped frame metric")
	}
	if metrics.RXQueueHighWater > 1 {
		t.Fatalf("RX queue high-water=%d, want bounded capacity 1", metrics.RXQueueHighWater)
	}
	if got, err := session.ReadFrame(); err != nil || got.Type != audiosocket.TypeSlin16 {
		t.Fatalf("latest RX frame type=%s err=%v", got.Type, err)
	}
	if err := session.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes / 2)}); err != nil {
		t.Fatalf("TX after RX overload: %v", err)
	}
	_ = peerTX.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := audiosocket.DecodeReader(peerTX); err != nil {
		t.Fatalf("TX socket closed after RX overload: %v", err)
	}
	if metrics.TXQueueHighWater > 1 {
		t.Fatalf("TX queue high-water=%d, want bounded capacity 1", metrics.TXQueueHighWater)
	}
}

// Reproduce the startup loss with TX fixed at 32: only RX capacity changes.
// Provider setup can delay the consumer. Four RX frames retain only the
// trailing silence; the baseline capacity retains the whole short utterance.
func TestRXStartupBudgetPreservesSpeechBeforeConsumer(t *testing.T) {
	for _, capacity := range []int{4, 32} {
		t.Run(fmt.Sprintf("RX_%d", capacity), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			a, err := New(ctx, Config{ParentDir: shortTempDir(t), BufferFrames: 32, RXBufferFrames: capacity})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			rxPath, txPath := a.SocketPaths()
			rx, tx, session := connectPair(t, a, rxPath, txPath)
			defer rx.Close()
			defer tx.Close()
			for i := 0; i < 27; i++ {
				pcm := make([]byte, 640)
				if i < 20 {
					for j := 0; j < len(pcm); j += 2 {
						binary.LittleEndian.PutUint16(pcm[j:], 1000)
					}
				}
				if err := writeSocketFrame(rx, audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: pcm}); err != nil {
					t.Fatal(err)
				}
			}
			for session.Metrics().RXFramesDropped+session.Metrics().RXQueueHighWater < 27 {
				select {
				case <-ctx.Done():
					t.Fatal("RX reader stalled")
				case <-time.After(time.Millisecond):
				}
			}
			kept := min(27, capacity)
			voiced := 0
			for i := 0; i < kept; i++ {
				frame, err := session.ReadFrameContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if binary.LittleEndian.Uint16(frame.Payload) != 0 {
					voiced++
				}
			}
			if capacity == 4 && (voiced != 0 || session.Metrics().RXFramesDropped != 23) {
				t.Fatal("four-frame startup regression was not reproduced")
			}
			if capacity == 32 && (voiced != 20 || session.Metrics().RXFramesDropped != 0) {
				t.Fatal("baseline budget lost early speech")
			}
		})
	}
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

func TestCall4MetricsSeparateRealAudioAndDegradedSilence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := New(context.Background(), Config{ParentDir: shortTempDir(t), BufferFrames: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rx, tx := a.SocketPaths()
	peerRX, peerTX, session := connectPair(t, a, rx, tx)
	defer peerRX.Close()
	defer peerTX.Close()
	defer session.Close()
	if err := session.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: pcmSamples(txFrameBytes / 2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := audiosocket.DecodeReader(peerTX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- session.ServeDegraded(ctx) }()
	frame, err := audiosocket.DecodeReader(peerTX)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.Payload, make([]byte, txFrameBytes)) {
		t.Fatal("degraded output was not silence")
	}
	deadline := time.Now().Add(time.Second)
	for session.Metrics().DegradedSilenceFrames == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m := session.Metrics()
	if m.RealAgentAudioFrames != 1 || m.DegradedSilenceFrames == 0 || m.LastRealAgentAudioAt == nil || m.DegradedModeStartedAt == nil {
		t.Fatalf("metrics=%+v", m)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("degraded drain leaked on cancellation")
	}
	// A fresh call must not inherit counters or timestamps.
	session.Close()
	peerRX.Close()
	peerTX.Close()
	nextRX, nextTX, next := connectPair(t, a, rx, tx)
	defer nextRX.Close()
	defer nextTX.Close()
	defer next.Close()
	if m = next.Metrics(); m.RealAgentAudioFrames != 0 || m.DegradedSilenceFrames != 0 || m.LastRealAgentAudioAt != nil {
		t.Fatalf("cross-call metrics: %+v", m)
	}
}
