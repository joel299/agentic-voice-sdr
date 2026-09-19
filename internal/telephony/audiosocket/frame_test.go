package audiosocket

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []Frame{
		{Type: TypeHangup, Payload: nil},
		{Type: TypeID, Payload: bytes.Repeat([]byte{0xab}, 16)},
		{Type: TypeDTMF, Payload: []byte{'1'}},
		{Type: TypeSlin, Payload: bytes.Repeat([]byte{0x01, 0x02}, 160)},
		{Type: TypeSlin16, Payload: bytes.Repeat([]byte{0x03, 0x04}, 320)},
		{Type: TypeError, Payload: []byte{0x01}},
	}

	for _, want := range cases {
		want := want
		t.Run(want.Type.String(), func(t *testing.T) {
			t.Parallel()
			encoded, err := Encode(want)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, n, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if n != len(encoded) {
				t.Fatalf("consumed %d, want %d", n, len(encoded))
			}
			if got.Type != want.Type {
				t.Fatalf("type=%v want=%v", got.Type, want.Type)
			}
			if !bytes.Equal(got.Payload, want.Payload) {
				t.Fatalf("payload mismatch")
			}
		})
	}
}

func TestDecodeReaderRoundTrip(t *testing.T) {
	t.Parallel()
	frame := Frame{Type: TypeSlin, Payload: bytes.Repeat([]byte{0xaa}, 320)}
	encoded, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != frame.Type || !bytes.Equal(got.Payload, frame.Payload) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

type chunkReader struct {
	data []byte
	step int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.step
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestDecodeReaderHandlesPartialReads(t *testing.T) {
	t.Parallel()
	want := Frame{Type: TypeDTMF, Payload: []byte("7")}
	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeReader(&chunkReader{data: encoded, step: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("partial-read mismatch: got=%+v want=%+v", got, want)
	}
}

func TestDecodeInvalidHeaderTooShort(t *testing.T) {
	t.Parallel()
	_, _, err := Decode([]byte{0x10, 0x00})
	if !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("err=%v want ErrInvalidHeader", err)
	}
}

func TestDecodeTruncatedPayload(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 3)
	buf[0] = byte(TypeSlin)
	binary.BigEndian.PutUint16(buf[1:3], 10)
	buf = append(buf, 0x01, 0x02, 0x03)
	_, _, err := Decode(buf)
	if !errors.Is(err, ErrTruncatedPayload) {
		t.Fatalf("err=%v want ErrTruncatedPayload", err)
	}
}

func TestDecodeReaderTruncatedPayload(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 3)
	buf[0] = byte(TypeSlin)
	binary.BigEndian.PutUint16(buf[1:3], 8)
	buf = append(buf, 0x01, 0x02)
	_, err := DecodeReader(bytes.NewReader(buf))
	if !errors.Is(err, ErrTruncatedPayload) {
		t.Fatalf("err=%v want ErrTruncatedPayload", err)
	}
}

func TestDecodeUnknownTypeRejected(t *testing.T) {
	t.Parallel()
	buf := []byte{0x55, 0x00, 0x00}
	_, _, err := Decode(buf)
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err=%v want ErrUnknownType", err)
	}
}

func TestSilenceTypeIsUnknown(t *testing.T) {
	t.Parallel()
	_, _, err := Decode([]byte{0x02, 0x00, 0x00})
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Decode err=%v want ErrUnknownType", err)
	}
	_, err = Encode(Frame{Type: FrameType(0x02)})
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Encode err=%v want ErrUnknownType", err)
	}
}

func TestControlFramePayloadLengths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		typ     FrameType
		valid   int
		invalid []int
	}{
		{name: "hangup", typ: TypeHangup, valid: 0, invalid: []int{1}},
		{name: "uuid", typ: TypeID, valid: 16, invalid: []int{15, 17}},
		{name: "dtmf", typ: TypeDTMF, valid: 1, invalid: []int{0, 2}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			valid := bytes.Repeat([]byte{0x01}, tc.valid)
			if _, err := Encode(Frame{Type: tc.typ, Payload: valid}); err != nil {
				t.Fatalf("valid Encode err=%v", err)
			}
			for _, n := range tc.invalid {
				payload := bytes.Repeat([]byte{0x01}, n)
				if _, err := Encode(Frame{Type: tc.typ, Payload: payload}); !errors.Is(err, ErrInvalidPayloadLength) {
					t.Errorf("Encode len=%d err=%v want ErrInvalidPayloadLength", n, err)
				}
				encoded := append([]byte{byte(tc.typ), byte(n >> 8), byte(n)}, payload...)
				if _, _, err := Decode(encoded); !errors.Is(err, ErrInvalidPayloadLength) {
					t.Errorf("Decode len=%d err=%v want ErrInvalidPayloadLength", n, err)
				}
				if _, err := DecodeReader(bytes.NewReader(encoded)); !errors.Is(err, ErrInvalidPayloadLength) {
					t.Errorf("DecodeReader len=%d err=%v want ErrInvalidPayloadLength", n, err)
				}
			}
		})
	}
}

func TestEncodeRejectsUnknownType(t *testing.T) {
	t.Parallel()
	_, err := Encode(Frame{Type: FrameType(0x55), Payload: nil})
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err=%v want ErrUnknownType", err)
	}
}

func TestEncodeRejectsPayloadOverMax(t *testing.T) {
	t.Parallel()
	payload := make([]byte, MaxPayloadSize+1)
	_, err := Encode(Frame{Type: TypeSlin, Payload: payload})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err=%v want ErrPayloadTooLarge", err)
	}
}

func TestDecodeRejectsPayloadOverMax(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 3)
	buf[0] = byte(TypeSlin)
	binary.BigEndian.PutUint16(buf[1:3], uint16(MaxPayloadSize+1))
	_, _, err := Decode(buf)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err=%v want ErrPayloadTooLarge", err)
	}
}

func TestDecodeDoesNotPanicOnGarbage(t *testing.T) {
	t.Parallel()
	inputs := [][]byte{
		nil,
		{},
		{0xff},
		{0x10, 0xff},
		{0x00, 0x00, 0x01},
		{0x01, 0x00, 0x10},
		append([]byte{0x10, 0x00, 0x04}, 0x01, 0x02, 0x03, 0x04, 0x05),
	}
	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d panicked: %v", i, r)
				}
			}()
			_, _, _ = Decode(in)
		}()
	}
}

func TestEncodeNilPayloadEqualsEmpty(t *testing.T) {
	t.Parallel()
	a, err := Encode(Frame{Type: TypeHangup, Payload: nil})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(Frame{Type: TypeHangup, Payload: []byte{}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("nil vs empty payload encoding differs")
	}
}

func FuzzDecode(f *testing.F) {
	seed, _ := Encode(Frame{Type: TypeSlin, Payload: []byte{0x01, 0x02, 0x03, 0x04}})
	f.Add(seed)
	f.Add([]byte{0x00, 0x00, 0x00})
	f.Add([]byte{0x55, 0x00, 0x01, 0xff})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v", r)
			}
		}()
		_, _, _ = Decode(data)
	})
}
