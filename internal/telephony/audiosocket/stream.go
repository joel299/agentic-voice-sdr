package audiosocket

import (
	"bytes"
	"io"
	"sync"
)

// Stream provides sequential AudioSocket frame transport over an io.Reader and
// io.Writer. It does not open or manage a network connection.
type Stream struct {
	reader io.Reader
	writer io.Writer

	closeOnce sync.Once
	closeErr  error
}

// NewStream creates a stream boundary over reader and writer. Either side may
// be nil when only reading or only writing is required.
func NewStream(reader io.Reader, writer io.Writer) *Stream {
	return &Stream{reader: reader, writer: writer}
}

// ReadFrame reads exactly one frame. EOF before any header byte is a clean
// disconnect; EOF after a partial header or payload is a truncation error.
func (s *Stream) ReadFrame() (Frame, error) {
	if s == nil || s.reader == nil {
		return Frame{}, io.ErrClosedPipe
	}

	header := make([]byte, HeaderSize)
	n, err := io.ReadFull(s.reader, header)
	if err != nil {
		if n == 0 && err == io.EOF {
			return Frame{}, io.EOF
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Frame{}, ErrInvalidHeader
		}
		return Frame{}, err
	}

	return DecodeReader(io.MultiReader(bytes.NewReader(header), s.reader))
}

// WriteFrame encodes and writes exactly one complete frame.
func (s *Stream) WriteFrame(frame Frame) error {
	if s == nil || s.writer == nil {
		return io.ErrClosedPipe
	}
	encoded, err := Encode(frame)
	if err != nil {
		return err
	}
	for len(encoded) > 0 {
		n, err := s.writer.Write(encoded)
		if n > 0 {
			encoded = encoded[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Close closes the underlying reader when it is closable, which is the
// production net.Conn path used by AudioSocket. It is idempotent and unblocks
// a ReadFrame waiting on that connection. For write-only streams it closes the
// underlying writer instead.
func (s *Stream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if closer, ok := s.reader.(io.Closer); ok {
			s.closeErr = closer.Close()
			return
		}
		if closer, ok := s.writer.(io.Closer); ok {
			s.closeErr = closer.Close()
		}
	})
	return s.closeErr
}
