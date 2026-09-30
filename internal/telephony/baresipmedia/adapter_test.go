package baresipmedia

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

func TestFixtureBidirectionalPCMContracts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, Config{ParentDir: t.TempDir(), BufferFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	fakeRX, err := dialUnix(t, rxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fakeRX.Close()
	fakeTX, err := dialUnix(t, txPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fakeTX.Close()
	ready, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := a.WaitConnected(ready); err != nil {
		t.Fatal(err)
	}

	rx := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: bytes.Repeat([]byte{0x34, 0x12}, 320)}
	if err := writeSocketFrame(fakeRX, rx); err != nil {
		t.Fatal(err)
	}
	gotRX, err := a.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if gotRX.Type != audiosocket.TypeSlin16 || len(gotRX.Payload) != 640 || !bytes.Equal(gotRX.Payload, rx.Payload) {
		t.Fatalf("RX contract mismatch: type=%s bytes=%d", gotRX.Type, len(gotRX.Payload))
	}

	tx := audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: bytes.Repeat([]byte{0x78, 0x56}, 480)}
	if err := a.WriteFrame(tx); err != nil {
		t.Fatal(err)
	}
	gotTX, err := audiosocket.DecodeReader(fakeTX)
	if err != nil {
		t.Fatal(err)
	}
	if gotTX.Type != audiosocket.TypeSlin24 || len(gotTX.Payload) != 960 || !bytes.Equal(gotTX.Payload, tx.Payload) {
		t.Fatalf("TX contract mismatch: type=%s bytes=%d", gotTX.Type, len(gotTX.Payload))
	}
}

func TestRejectsWrongRateTypeAndUnalignedPCM(t *testing.T) {
	a := &Adapter{txFrames: make(chan audiosocket.Frame, 1), done: make(chan struct{})}
	for _, frame := range []audiosocket.Frame{
		{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2}},
		{Type: audiosocket.TypeSlin24, Payload: []byte{1}},
		{Type: audiosocket.TypeSlin24},
	} {
		if err := a.WriteFrame(frame); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("WriteFrame(%s, %d) error = %v, want ErrInvalidFormat", frame.Type, len(frame.Payload), err)
		}
	}
}

func TestRXQueueOverflowFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, Config{ParentDir: t.TempDir(), BufferFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	fakeRX, err := dialUnix(t, rxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fakeRX.Close()
	fakeTX, err := dialUnix(t, txPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fakeTX.Close()
	ready, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := a.WaitConnected(ready); err != nil {
		t.Fatal(err)
	}
	frame := audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: bytes.Repeat([]byte{0, 0}, 320)}
	for range 8 {
		if err := writeSocketFrame(fakeRX, frame); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("RX queue did not fail closed after bounded overflow")
	}
	if !errors.Is(a.terminalError(), ErrBackpressure) {
		t.Fatalf("terminal error = %v", a.terminalError())
	}
}

func TestCancellationClosesSocketsAndCleansPaths(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := New(ctx, Config{ParentDir: parent})
	if err != nil {
		t.Fatal(err)
	}
	dir := a.dir
	cancel()
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close adapter")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, RXSocket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected RX socket: %v", err)
	}
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
