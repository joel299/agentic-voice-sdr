package audiosocket

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type oneByteReader struct {
	data []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) {
	return 0, errors.New("writer failed")
}

func TestStreamReadsMultipleFramesWithoutConsumingNextFrame(t *testing.T) {
	first := Frame{Type: TypeDTMF, Payload: []byte("1")}
	second := Frame{Type: TypeHangup}
	firstBytes, err := Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := Encode(second)
	if err != nil {
		t.Fatal(err)
	}
	stream := NewStream(bytes.NewReader(append(firstBytes, secondBytes...)), io.Discard)

	got, err := stream.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != first.Type || !bytes.Equal(got.Payload, first.Payload) {
		t.Fatalf("first frame=%+v want=%+v", got, first)
	}
	got, err = stream.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != second.Type || len(got.Payload) != 0 {
		t.Fatalf("second frame=%+v want=%+v", got, second)
	}
}

func TestStreamSupportsPartialReads(t *testing.T) {
	frame := Frame{Type: TypeDTMF, Payload: []byte("7")}
	encoded, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	stream := NewStream(&oneByteReader{data: encoded}, io.Discard)
	got, err := stream.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != frame.Type || !bytes.Equal(got.Payload, frame.Payload) {
		t.Fatalf("got=%+v want=%+v", got, frame)
	}
}

func TestStreamReturnsCleanEOFBetweenFrames(t *testing.T) {
	stream := NewStream(bytes.NewReader(nil), io.Discard)
	_, err := stream.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err=%v want io.EOF", err)
	}
}

func TestStreamReturnsHeaderTruncation(t *testing.T) {
	stream := NewStream(bytes.NewReader([]byte{byte(TypeDTMF)}), io.Discard)
	_, err := stream.ReadFrame()
	if !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("err=%v want ErrInvalidHeader", err)
	}
}

func TestStreamReturnsPayloadTruncation(t *testing.T) {
	stream := NewStream(bytes.NewReader([]byte{byte(TypeDTMF), 0x00, 0x01}), io.Discard)
	_, err := stream.ReadFrame()
	if !errors.Is(err, ErrTruncatedPayload) {
		t.Fatalf("err=%v want ErrTruncatedPayload", err)
	}
}

func TestStreamRejectsUnknownFrame(t *testing.T) {
	stream := NewStream(bytes.NewReader([]byte{0x55, 0x00, 0x00}), io.Discard)
	_, err := stream.ReadFrame()
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err=%v want ErrUnknownType", err)
	}
}

func TestStreamRejectsMalformedFrame(t *testing.T) {
	stream := NewStream(bytes.NewReader([]byte{byte(TypeDTMF), 0x00, 0x02, 0x01, 0x02}), io.Discard)
	_, err := stream.ReadFrame()
	if !errors.Is(err, ErrInvalidPayloadLength) {
		t.Fatalf("err=%v want ErrInvalidPayloadLength", err)
	}
}

func TestStreamWriteFrameWritesCompleteFrame(t *testing.T) {
	var out bytes.Buffer
	stream := NewStream(bytes.NewReader(nil), &out)
	frame := Frame{Type: TypeDTMF, Payload: []byte("5")}
	if err := stream.WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	want, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("written=%x want=%x", out.Bytes(), want)
	}
}

func TestStreamPropagatesWriterError(t *testing.T) {
	stream := NewStream(bytes.NewReader(nil), failWriter{})
	if err := stream.WriteFrame(Frame{Type: TypeHangup}); err == nil || err.Error() != "writer failed" {
		t.Fatalf("err=%v want writer failed", err)
	}
}

func TestStreamCloseUnblocksBlockedReadAndIsIdempotent(t *testing.T) {
	peer, conn := net.Pipe()
	defer peer.Close()
	stream := NewStream(conn, conn)
	readDone := make(chan error, 1)
	go func() {
		_, err := stream.ReadFrame()
		readDone <- err
	}()
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("blocked ReadFrame returned nil after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock ReadFrame")
	}
}

func TestStreamInvalidInputDoesNotPanic(t *testing.T) {
	inputs := [][]byte{nil, {}, {0x10}, {0x55, 0x00, 0x01}, {byte(TypeDTMF), 0xff, 0xff}}
	for i, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("input %d panicked: %v", i, recovered)
				}
			}()
			_, _ = NewStream(bytes.NewReader(input), io.Discard).ReadFrame()
		}()
	}
}
