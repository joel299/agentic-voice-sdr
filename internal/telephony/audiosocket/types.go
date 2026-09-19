package audiosocket

import "fmt"

// HeaderSize is the fixed AudioSocket header length in bytes.
const HeaderSize = 3

// MaxPayloadSize is the maximum accepted payload length in bytes.
// Chosen to fit the largest common SLIN 20 ms chunk (192 kHz) with headroom,
// while staying well under the uint16 wire maximum (65535).
const MaxPayloadSize = 16384

// FrameType is the AudioSocket message type indicator (1 byte).
type FrameType byte

const (
	TypeHangup  FrameType = 0x00
	TypeID      FrameType = 0x01
	TypeSilence FrameType = 0x02
	TypeDTMF    FrameType = 0x03
	TypeSlin    FrameType = 0x10 // 8 kHz signed-linear PCM
	TypeSlin12  FrameType = 0x11
	TypeSlin16  FrameType = 0x12 // 16 kHz
	TypeSlin24  FrameType = 0x13
	TypeSlin32  FrameType = 0x14
	TypeSlin44  FrameType = 0x15
	TypeSlin48  FrameType = 0x16
	TypeSlin96  FrameType = 0x17
	TypeSlin192 FrameType = 0x18
	TypeError   FrameType = 0xff
)

func (t FrameType) String() string {
	switch t {
	case TypeHangup:
		return "hangup"
	case TypeID:
		return "id"
	case TypeSilence:
		return "silence"
	case TypeDTMF:
		return "dtmf"
	case TypeSlin:
		return "slin"
	case TypeSlin12:
		return "slin12"
	case TypeSlin16:
		return "slin16"
	case TypeSlin24:
		return "slin24"
	case TypeSlin32:
		return "slin32"
	case TypeSlin44:
		return "slin44"
	case TypeSlin48:
		return "slin48"
	case TypeSlin96:
		return "slin96"
	case TypeSlin192:
		return "slin192"
	case TypeError:
		return "error"
	default:
		return fmt.Sprintf("unknown(0x%02x)", byte(t))
	}
}

// Known reports whether t is a recognized AudioSocket frame type.
func (t FrameType) Known() bool {
	switch t {
	case TypeHangup, TypeID, TypeSilence, TypeDTMF,
		TypeSlin, TypeSlin12, TypeSlin16, TypeSlin24, TypeSlin32,
		TypeSlin44, TypeSlin48, TypeSlin96, TypeSlin192,
		TypeError:
		return true
	default:
		return false
	}
}

// Frame is one AudioSocket message.
type Frame struct {
	Type    FrameType
	Payload []byte
}
