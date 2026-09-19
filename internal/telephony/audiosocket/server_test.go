package audiosocket

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errHandler = errors.New("handler failed")

func serveInBackground(t *testing.T, server *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(ctx) }()
	return cancel, errs
}

func waitForServerAddr(t *testing.T, server *Server) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != nil {
			return addr.String()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not bind")
	return ""
}

func testFrame(t *testing.T, typ FrameType, payload []byte) []byte {
	t.Helper()
	encoded, err := Encode(Frame{Type: typ, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestServerBindsProvidedAddress(t *testing.T) {
	server := NewServer("127.0.0.1:0", func(_ context.Context, _ *Stream) error { return nil })
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	addr := server.Addr()
	if addr == nil || addr.Network() != "tcp" || addr.String() == "" {
		t.Fatalf("unexpected address: %#v", addr)
	}
}

func TestServerAcceptsLoopbackAndHandlerReceivesStream(t *testing.T) {
	frames := make(chan Frame, 1)
	server := NewServer("127.0.0.1:0", func(_ context.Context, stream *Stream) error {
		frame, err := stream.ReadFrame()
		if err != nil {
			return err
		}
		frames <- frame
		return nil
	})
	cancel, errs := serveInBackground(t, server)
	defer cancel()
	defer server.Close()

	conn, err := net.Dial("tcp", waitForServerAddr(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write(testFrame(t, TypeSlin16, []byte{1, 2})); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	select {
	case frame := <-frames:
		if frame.Type != TypeSlin16 || len(frame.Payload) != 2 {
			t.Fatalf("unexpected frame: %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive stream")
	}
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServerHandlesMultipleConnectionsSequentially(t *testing.T) {
	var count atomic.Int32
	server := NewServer("127.0.0.1:0", func(_ context.Context, stream *Stream) error {
		if _, err := stream.ReadFrame(); err != nil {
			return err
		}
		count.Add(1)
		return nil
	})
	cancel, errs := serveInBackground(t, server)
	defer cancel()
	defer server.Close()

	for i := 0; i < 2; i++ {
		conn, err := net.Dial("tcp", waitForServerAddr(t, server))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Write(testFrame(t, TypeDTMF, []byte{'1'})); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	deadline := time.Now().Add(time.Second)
	for count.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if count.Load() != 2 {
		t.Fatalf("handled %d connections", count.Load())
	}
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestServerContextCancellationStopsListener(t *testing.T) {
	server := NewServer("127.0.0.1:0", func(_ context.Context, _ *Stream) error { return nil })
	cancel, errs := serveInBackground(t, server)
	addr := waitForServerAddr(t, server)
	cancel()
	if err := <-errs; err != nil {
		t.Fatalf("serve returned %v", err)
	}
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener accepted after cancellation")
	}
}

func TestServerShutdownGracefullyClosesActiveConnection(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan error, 1)
	server := NewServer("127.0.0.1:0", func(_ context.Context, stream *Stream) error {
		close(started)
		_, err := stream.ReadFrame()
		finished <- err
		return nil
	})
	_, errs := serveInBackground(t, server)
	conn, err := net.Dial("tcp", waitForServerAddr(t, server))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-errs; err != nil {
		t.Fatalf("serve returned %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish during shutdown")
	}
}

func TestServerPropagatesHandlerErrorAndClosesConnection(t *testing.T) {
	server := NewServer("127.0.0.1:0", func(_ context.Context, _ *Stream) error { return errHandler })
	_, errs := serveInBackground(t, server)
	conn, err := net.Dial("tcp", waitForServerAddr(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !errors.Is(err, errHandler) {
		t.Fatalf("expected handler error, got %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := conn.Read(one[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("expected closed connection, got %v", err)
	}
	_ = conn.Close()
}

func TestServerPropagatesListenAndAcceptErrors(t *testing.T) {
	server := NewServer("bad-address", func(_ context.Context, _ *Stream) error { return nil })
	if err := server.Listen(); err == nil {
		t.Fatal("expected listen error")
	}

	server = NewServer("127.0.0.1:0", func(_ context.Context, _ *Stream) error { return nil })
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := server.listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("expected accept error")
	}
}

func TestServerHandlesAbruptDisconnectWithoutPanic(t *testing.T) {
	called := make(chan struct{})
	server := NewServer("127.0.0.1:0", func(_ context.Context, stream *Stream) error {
		defer close(called)
		_, _ = stream.ReadFrame()
		return nil
	})
	cancel, errs := serveInBackground(t, server)
	defer cancel()
	defer server.Close()
	conn, err := net.Dial("tcp", waitForServerAddr(t, server))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("handler did not process disconnect")
	}
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestServerInvalidInputDoesNotPanic(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	server := NewServer("127.0.0.1:0", func(_ context.Context, stream *Stream) error {
		defer wg.Done()
		_, _ = stream.ReadFrame()
		return nil
	})
	cancel, errs := serveInBackground(t, server)
	defer cancel()
	defer server.Close()
	conn, err := net.Dial("tcp", waitForServerAddr(t, server))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte{0x02, 0x00, 0x00})
	_ = conn.Close()
	wait := make(chan struct{})
	go func() { wg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("handler did not return for invalid input")
	}
	cancel()
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}
