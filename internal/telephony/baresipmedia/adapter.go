// Package baresipmedia connects Baresip's external audio modules to the
// provider-neutral AudioSocket frame contract over private Unix sockets.
package baresipmedia

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
)

var (
	ErrClosed        = errors.New("baresip media adapter: closed")
	ErrNotConnected  = errors.New("baresip media adapter: Baresip audio module is not connected")
	ErrBackpressure  = errors.New("baresip media adapter: bounded frame queue is full")
	ErrInvalidFormat = errors.New("baresip media adapter: frame does not match PCM contract")
)

const (
	RXSocket            = "rx.sock" // Baresip auplay (remote RTP) -> Go
	TXSocket            = "tx.sock" // Go -> Baresip ausrc (local RTP)
	defaultBufferFrames = 8
	maxBufferFrames     = 256
)

type Config struct {
	// The adapter creates and removes its own 0700 subdirectory inside ParentDir.
	ParentDir    string
	BufferFrames int
}

// Adapter implements bridge.AudioReader for inbound 16 kHz PCM and
// bridge.AudioWriter for outbound 24 kHz PCM. Both queues have fixed capacity.
type Adapter struct {
	dir      string
	rxPath   string
	txPath   string
	rxListen *net.UnixListener
	txListen *net.UnixListener
	rxConn   *net.UnixConn
	txConn   *net.UnixConn
	rxFrames chan audiosocket.Frame
	txFrames chan audiosocket.Frame
	ready    chan struct{}
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc

	mu       sync.Mutex
	closed   bool
	err      error
	wg       sync.WaitGroup
	stopOnce sync.Once
}

var _ bridge.AudioReader = (*Adapter)(nil)
var _ bridge.AudioWriter = (*Adapter)(nil)

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.BufferFrames == 0 {
		cfg.BufferFrames = defaultBufferFrames
	}
	if cfg.BufferFrames < 1 || cfg.BufferFrames > maxBufferFrames {
		return nil, fmt.Errorf("buffer frames must be between 1 and %d", maxBufferFrames)
	}
	dir, err := os.MkdirTemp(cfg.ParentDir, "baresip-media-")
	if err != nil {
		return nil, fmt.Errorf("create private media directory: %w", err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("protect media directory: %w", err)
	}
	aCtx, cancel := context.WithCancel(ctx)
	a := &Adapter{
		dir: dir, rxPath: filepath.Join(dir, RXSocket), txPath: filepath.Join(dir, TXSocket),
		rxFrames: make(chan audiosocket.Frame, cfg.BufferFrames),
		txFrames: make(chan audiosocket.Frame, cfg.BufferFrames),
		ready:    make(chan struct{}), done: make(chan struct{}), ctx: aCtx, cancel: cancel,
	}
	if a.rxListen, err = listenUnix(a.rxPath); err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("listen for Baresip RX: %w", err)
	}
	if a.txListen, err = listenUnix(a.txPath); err != nil {
		_ = a.rxListen.Close()
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("listen for Baresip TX: %w", err)
	}
	a.wg.Add(2)
	go a.accept(a.rxListen, true)
	go a.accept(a.txListen, false)
	context.AfterFunc(aCtx, func() { a.stop(context.Cause(aCtx)) })
	return a, nil
}

func listenUnix(path string) (*net.UnixListener, error) {
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return l, nil
}

func (a *Adapter) accept(l *net.UnixListener, rx bool) {
	defer a.wg.Done()
	conn, err := l.AcceptUnix()
	if err != nil {
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if !closed {
			a.stop(fmt.Errorf("accept Baresip audio connection: %w", err))
		}
		return
	}
	_ = l.Close()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	if rx {
		a.rxConn = conn
	} else {
		a.txConn = conn
	}
	if a.rxConn != nil && a.txConn != nil {
		close(a.ready)
		a.wg.Add(2)
		go a.readRX()
		go a.writeTX()
	}
	a.mu.Unlock()
}

// WaitConnected waits until both Baresip audio callbacks have connected.
func (a *Adapter) WaitConnected(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.ready:
		return nil
	case <-a.done:
		return a.terminalError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SocketPaths returns the private local IPC endpoints for a Baresip audio
// module. Callers should pass rxPath to auplay and txPath to ausrc.
func (a *Adapter) SocketPaths() (rxPath, txPath string) { return a.rxPath, a.txPath }

func (a *Adapter) ReadFrame() (audiosocket.Frame, error) {
	select {
	case frame := <-a.rxFrames:
		return frame, nil
	case <-a.done:
		return audiosocket.Frame{}, a.terminalError()
	}
}

func (a *Adapter) WriteFrame(frame audiosocket.Frame) error {
	if err := validateFrame(frame, audiosocket.TypeSlin24); err != nil {
		return err
	}
	select {
	case <-a.ready:
	case <-a.done:
		return a.terminalError()
	}
	select {
	case <-a.done:
		return a.terminalError()
	case a.txFrames <- cloneFrame(frame):
		return nil
	default:
		a.stop(ErrBackpressure)
		return ErrBackpressure
	}
}

func (a *Adapter) readRX() {
	defer a.wg.Done()
	for {
		frame, err := audiosocket.DecodeReader(a.rxConn)
		if err != nil {
			if a.ctx.Err() == nil {
				a.stop(fmt.Errorf("read Baresip RX: %w", err))
			}
			return
		}
		if err = validateFrame(frame, audiosocket.TypeSlin16); err != nil {
			a.stop(err)
			return
		}
		select {
		case a.rxFrames <- frame:
		case <-a.done:
			return
		default:
			a.stop(ErrBackpressure)
			return
		}
	}
}

func (a *Adapter) writeTX() {
	defer a.wg.Done()
	for {
		select {
		case <-a.done:
			return
		case frame := <-a.txFrames:
			wire, err := audiosocket.Encode(frame)
			if err == nil {
				err = writeFull(a.txConn, wire)
				if err == nil {
					continue
				}
			}
			if a.ctx.Err() == nil {
				a.stop(fmt.Errorf("write Baresip TX: %w", err))
			}
			return
		}
	}
}

func writeFull(conn net.Conn, wire []byte) error {
	for len(wire) > 0 {
		n, err := conn.Write(wire)
		if err != nil {
			return err
		}
		if n == 0 {
			return net.ErrClosed
		}
		wire = wire[n:]
	}
	return nil
}

func cloneFrame(frame audiosocket.Frame) audiosocket.Frame {
	frame.Payload = append([]byte(nil), frame.Payload...)
	return frame
}

func validateFrame(frame audiosocket.Frame, want audiosocket.FrameType) error {
	if frame.Type != want || len(frame.Payload) == 0 || len(frame.Payload)%2 != 0 {
		return fmt.Errorf("%w: got %s with %d bytes, want %s PCM16", ErrInvalidFormat, frame.Type, len(frame.Payload), want)
	}
	return nil
}

func (a *Adapter) stop(err error) {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		if err == nil {
			err = ErrClosed
		}
		a.err = err
		rxListen, txListen := a.rxListen, a.txListen
		rxConn, txConn := a.rxConn, a.txConn
		a.mu.Unlock()
		a.cancel()
		close(a.done)
		if rxListen != nil {
			_ = rxListen.Close()
		}
		if txListen != nil {
			_ = txListen.Close()
		}
		if rxConn != nil {
			_ = rxConn.Close()
		}
		if txConn != nil {
			_ = txConn.Close()
		}
		_ = os.Remove(a.rxPath)
		_ = os.Remove(a.txPath)
	})
}

func (a *Adapter) terminalError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	return ErrClosed
}

func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	a.stop(nil)
	a.wg.Wait()
	_ = os.RemoveAll(a.dir)
	return nil
}
