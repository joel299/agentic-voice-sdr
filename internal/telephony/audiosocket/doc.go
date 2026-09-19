// Package audiosocket implements the Asterisk AudioSocket frame codec boundary.
//
// Wire format (big-endian):
//
//	| 1 byte type | 2 bytes payload length | N bytes payload |
//
// This package is intentionally I/O-free aside from optional io.Reader decode
// helpers. It never dials SIP, opens production TCP servers, or touches Gemini,
// Redis, RabbitMQ, or databases.
//
// Limits:
//
//   - HeaderSize is always 3 bytes.
//   - MaxPayloadSize caps payload length to prevent unbounded allocation.
//     The wire length field is uint16, so the theoretical maximum is 65535;
//     MaxPayloadSize is set below that for defense-in-depth on the PCM path
//     (covers up to ~192 kHz * 20 ms * 2 bytes SLIN chunks with headroom).
//
// Unknown frame types are rejected (ErrUnknownType) on both encode and decode.
package audiosocket
