package audiosocket

import (
	"encoding/binary"
	"io"
)

// Encode serializes a Frame to AudioSocket wire bytes.
func Encode(f Frame) ([]byte, error) {
	if !f.Type.Known() {
		return nil, ErrUnknownType
	}
	n := len(f.Payload)
	if n > MaxPayloadSize {
		return nil, ErrPayloadTooLarge
	}
	if n > 0xffff {
		return nil, ErrPayloadTooLarge
	}
	if err := validatePayloadLength(f.Type, n); err != nil {
		return nil, err
	}
	out := make([]byte, HeaderSize+n)
	out[0] = byte(f.Type)
	binary.BigEndian.PutUint16(out[1:3], uint16(n))
	if n > 0 {
		copy(out[HeaderSize:], f.Payload)
	}
	return out, nil
}

// Decode parses one AudioSocket frame from b.
// It returns the frame, the number of bytes consumed, and an error.
// Invalid input never panics; it returns a typed error.
func Decode(b []byte) (Frame, int, error) {
	if len(b) < HeaderSize {
		return Frame{}, 0, ErrInvalidHeader
	}
	ft := FrameType(b[0])
	if !ft.Known() {
		return Frame{}, 0, ErrUnknownType
	}
	payloadLen := int(binary.BigEndian.Uint16(b[1:3]))
	if payloadLen > MaxPayloadSize {
		return Frame{}, 0, ErrPayloadTooLarge
	}
	if err := validatePayloadLength(ft, payloadLen); err != nil {
		return Frame{}, 0, err
	}
	total := HeaderSize + payloadLen
	if len(b) < total {
		return Frame{}, 0, ErrTruncatedPayload
	}
	var payload []byte
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		copy(payload, b[HeaderSize:total])
	}
	return Frame{Type: ft, Payload: payload}, total, nil
}

// DecodeReader reads exactly one frame from r.
func DecodeReader(r io.Reader) (Frame, error) {
	hdr := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Frame{}, ErrInvalidHeader
		}
		return Frame{}, err
	}
	ft := FrameType(hdr[0])
	if !ft.Known() {
		return Frame{}, ErrUnknownType
	}
	payloadLen := int(binary.BigEndian.Uint16(hdr[1:3]))
	if payloadLen > MaxPayloadSize {
		return Frame{}, ErrPayloadTooLarge
	}
	if err := validatePayloadLength(ft, payloadLen); err != nil {
		return Frame{}, err
	}
	var payload []byte
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return Frame{}, ErrTruncatedPayload
			}
			return Frame{}, err
		}
	}
	return Frame{Type: ft, Payload: payload}, nil
}

func validatePayloadLength(ft FrameType, n int) error {
	switch ft {
	case TypeHangup:
		if n != 0 {
			return ErrInvalidPayloadLength
		}
	case TypeID:
		if n != 16 {
			return ErrInvalidPayloadLength
		}
	case TypeDTMF:
		if n != 1 {
			return ErrInvalidPayloadLength
		}
	}
	return nil
}
