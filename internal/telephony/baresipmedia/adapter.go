// Package baresipmedia connects Baresip's external audio modules to the
// provider-neutral AudioSocket frame contract over private Unix sockets.
package baresipmedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
)

var (
	ErrClosed        = errors.New("baresip media adapter: closed")
	ErrNotConnected  = errors.New("baresip media adapter: Baresip audio module is not connected")
	ErrSessionClosed = errors.New("baresip media adapter: call media session closed")
	ErrBackpressure  = errors.New("baresip media adapter: bounded RX frame queue is full")
	ErrInvalidFormat = errors.New("baresip media adapter: frame does not match PCM contract")
)

const (
	RXSocket            = "rx.sock" // Baresip auplay (remote RTP) -> Go
	TXSocket            = "tx.sock" // Go -> Baresip ausrc (local RTP)
	defaultBufferFrames = 32
	maxBufferFrames     = 256
	pairTimeout         = 5 * time.Second
	ptimeMillis         = 20
	rxRate              = 16000
	txRate              = 24000
	txFrameBytes        = txRate * ptimeMillis / 1000 * 2
)

type Config struct {
	// The adapter creates and removes its own 0700 subdirectory inside ParentDir.
	ParentDir string
	// BufferFrames bounds complete 20 ms frames in each direction per call.
	BufferFrames int
}

// Adapter owns stable listeners and admits one Baresip RX/TX pair at a time.
// Prefer WaitSession for each call so bridge.Close closes only that call's
// media session; Adapter.Close is reserved for shutting down the long-running
// media service.
type Adapter struct {
	dir      string
	rxPath   string
	txPath   string
	rxListen *net.UnixListener
	txListen *net.UnixListener
	rxFrames int
	txFrames int
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	closed    bool
	err       error
	rxPending *net.UnixConn
	txPending *net.UnixConn
	pairTimer *time.Timer
	pairEpoch uint64
	active    *mediaSession
	changed   chan struct{}
	wg        sync.WaitGroup
	stopOnce  sync.Once
}

// Session scopes audio queues and connections to one call. It implements the
// existing bridge interfaces; its Close detaches that call but preserves the
// Adapter listeners and stable socket paths for the next call.
type Session struct {
	adapter *Adapter
	state   *mediaSession
}

type txFrame struct {
	audiosocket.Frame
	degraded bool
}

type mediaSession struct {
	adapter *Adapter
	rxConn  *net.UnixConn
	txConn  *net.UnixConn
	rxQueue chan audiosocket.Frame
	txQueue chan txFrame
	done    chan struct{}
	endOnce sync.Once
	rxOnce  sync.Once
	wg      sync.WaitGroup

	errMu   sync.Mutex
	err     error
	metrics sessionMetrics

	txMu      sync.Mutex
	txPartial [txFrameBytes]byte
	txUsed    int
}

type sessionMetrics struct {
	rxFramesDropped       atomic.Uint64
	rxQueueHighWater      atomic.Uint64
	txQueueHighWater      atomic.Uint64
	txWaitCount           atomic.Uint64
	txWaitDurationNS      atomic.Uint64
	realAgentAudioFrames  atomic.Uint64
	degradedSilenceFrames atomic.Uint64
	lastRealAudioNS       atomic.Int64
	degradedStartedNS     atomic.Int64
}

// SessionMetrics contains counters for one call-scoped media session.
type SessionMetrics struct {
	RXFramesDropped       uint64     `json:"rx_frames_dropped"`
	RXQueueHighWater      uint64     `json:"rx_queue_high_water"`
	TXQueueHighWater      uint64     `json:"tx_queue_high_water"`
	TXWaitCount           uint64     `json:"tx_wait_count"`
	TXWaitDurationMS      uint64     `json:"tx_wait_duration_ms"`
	RealAgentAudioFrames  uint64     `json:"real_agent_audio_frames"`
	DegradedSilenceFrames uint64     `json:"degraded_silence_frames"`
	LastRealAgentAudioAt  *time.Time `json:"last_real_agent_audio_at"`
	DegradedModeStartedAt *time.Time `json:"degraded_mode_started_at"`
}

var (
	_ bridge.AudioReader = (*Adapter)(nil)
	_ bridge.AudioWriter = (*Adapter)(nil)
	_ bridge.AudioReader = (*Session)(nil)
	_ bridge.AudioWriter = (*Session)(nil)
	_ io.Closer          = (*Session)(nil)
)

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
		rxFrames: cfg.BufferFrames, txFrames: cfg.BufferFrames,
		done: make(chan struct{}), ctx: aCtx, cancel: cancel,
		changed: make(chan struct{}),
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
	go a.acceptLoop(a.rxListen, true)
	go a.acceptLoop(a.txListen, false)
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

func (a *Adapter) acceptLoop(l *net.UnixListener, rx bool) {
	defer a.wg.Done()
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			a.mu.Lock()
			closed := a.closed
			a.mu.Unlock()
			if !closed {
				a.stop(fmt.Errorf("accept Baresip media connection: %w", err))
			}
			return
		}
		a.admit(conn, rx)
	}
}

func (a *Adapter) admit(conn *net.UnixConn, rx bool) {
	var start *mediaSession
	a.mu.Lock()
	active := a.active != nil
	if active {
		select {
		case <-a.active.done:
			active = false
		default:
		}
	}
	if a.closed || active || (rx && a.rxPending != nil) || (!rx && a.txPending != nil) {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	wasEmpty := a.rxPending == nil && a.txPending == nil
	if rx {
		a.rxPending = conn
	} else {
		a.txPending = conn
	}
	if wasEmpty {
		a.pairEpoch++
		epoch := a.pairEpoch
		a.pairTimer = time.AfterFunc(pairTimeout, func() { a.expirePending(epoch) })
	}
	if a.active == nil && a.rxPending != nil && a.txPending != nil {
		if a.pairTimer != nil {
			a.pairTimer.Stop()
			a.pairTimer = nil
		}
		a.pairEpoch++
		start = a.activatePendingLocked()
	}
	a.mu.Unlock()
	if start != nil {
		a.startSession(start)
	}
}

func (a *Adapter) expirePending(epoch uint64) {
	a.mu.Lock()
	if a.closed || a.pairEpoch != epoch {
		a.mu.Unlock()
		return
	}
	rxPending, txPending := a.rxPending, a.txPending
	a.rxPending, a.txPending, a.pairTimer = nil, nil, nil
	a.pairEpoch++
	a.notifyLocked()
	a.mu.Unlock()
	if rxPending != nil {
		_ = rxPending.Close()
	}
	if txPending != nil {
		_ = txPending.Close()
	}
}

func (a *Adapter) activatePendingLocked() *mediaSession {
	s := &mediaSession{
		adapter: a,
		rxConn:  a.rxPending,
		txConn:  a.txPending,
		rxQueue: make(chan audiosocket.Frame, a.rxFrames),
		txQueue: make(chan txFrame, a.txFrames),
		done:    make(chan struct{}),
	}
	a.rxPending, a.txPending, a.active = nil, nil, s
	s.wg.Add(2)
	a.notifyLocked()
	return s
}

func (a *Adapter) startSession(s *mediaSession) {
	go a.readRX(s)
	go a.writeTX(s)
}

func (a *Adapter) notifyLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// WaitSession waits for the next complete Baresip RX/TX pair and returns a
// call-scoped bridge endpoint. Closing the returned Session preserves Adapter.
func (a *Adapter) WaitSession(ctx context.Context) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a.mu.Lock()
		if a.closed {
			err := a.err
			if err == nil {
				err = ErrClosed
			}
			a.mu.Unlock()
			return nil, err
		}
		if s := a.active; s != nil {
			select {
			case <-s.done:
				changed := a.changed
				a.mu.Unlock()
				select {
				case <-changed:
					continue
				case <-a.done:
					return nil, a.terminalError()
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			default:
				a.mu.Unlock()
				return &Session{adapter: a, state: s}, nil
			}
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-changed:
		case <-a.done:
			return nil, a.terminalError()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// WaitConnected is retained for startup probes and waits for a complete pair.
func (a *Adapter) WaitConnected(ctx context.Context) error {
	_, err := a.WaitSession(ctx)
	return err
}

// SocketPaths returns stable Unix socket paths for the lifetime of Adapter.
func (a *Adapter) SocketPaths() (rxPath, txPath string) { return a.rxPath, a.txPath }

// ReadFrame and WriteFrame delegate to the active call for compatibility with
// the bridge interfaces. Per-call bridges should use WaitSession so their
// Close operation does not shut down the long-running Adapter.
func (a *Adapter) ReadFrame() (audiosocket.Frame, error) {
	s, err := a.WaitSession(context.Background())
	if err != nil {
		return audiosocket.Frame{}, err
	}
	return s.ReadFrame()
}

func (a *Adapter) WriteFrame(frame audiosocket.Frame) error {
	s, err := a.currentSession()
	if err != nil {
		return err
	}
	return s.WriteFrame(frame)
}

func (a *Adapter) currentSession() (*Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, a.terminalErrorLocked()
	}
	if a.active == nil {
		return nil, ErrNotConnected
	}
	select {
	case <-a.active.done:
		return nil, ErrNotConnected
	default:
	}
	return &Session{adapter: a, state: a.active}, nil
}

func (s *Session) ReadFrame() (audiosocket.Frame, error) {
	return s.ReadFrameContext(context.Background())
}

func (s *Session) ReadFrameContext(ctx context.Context) (audiosocket.Frame, error) {
	if s == nil || s.state == nil {
		return audiosocket.Frame{}, ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := s.state
	select {
	case <-state.done:
		return s.endedFrame()
	default:
	}
	select {
	case frame := <-state.rxQueue:
		select {
		case <-state.done:
			return s.endedFrame()
		default:
			return frame, nil
		}
	case <-state.done:
		return s.endedFrame()
	case <-ctx.Done():
		return audiosocket.Frame{}, ctx.Err()
	}
}

func (s *Session) endedFrame() (audiosocket.Frame, error) {
	state := s.state
	state.errMu.Lock()
	err := state.err
	state.errMu.Unlock()
	if err != nil && !isNormalDisconnect(err) && !errors.Is(err, ErrSessionClosed) {
		return audiosocket.Frame{}, err
	}
	returned := false
	state.rxOnce.Do(func() { returned = true })
	if returned {
		return audiosocket.Frame{Type: audiosocket.TypeHangup}, nil
	}
	return audiosocket.Frame{}, io.EOF
}

func (s *Session) WriteFrame(frame audiosocket.Frame) error {
	return s.WriteFrameContext(context.Background(), frame)
}

func (s *Session) WriteFrameContext(ctx context.Context, frame audiosocket.Frame) error {
	if s == nil || s.state == nil {
		return ErrSessionClosed
	}
	if err := validateVariableFrame(frame, audiosocket.TypeSlin24); err != nil {
		return err
	}
	return s.state.enqueuePCM(ctx, frame.Payload)
}

func (s *Session) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	s.adapter.endSession(s.state, ErrSessionClosed)
	return nil
}

func validateFrame(frame audiosocket.Frame, want audiosocket.FrameType) error {
	if frame.Type != want || len(frame.Payload) == 0 || len(frame.Payload)%2 != 0 {
		return fmt.Errorf("%w: got %s with %d bytes, want %s PCM16", ErrInvalidFormat, frame.Type, len(frame.Payload), want)
	}
	if len(frame.Payload) > audiosocket.MaxPayloadSize {
		return fmt.Errorf("%w: PCM chunk has %d bytes, maximum is %d", ErrInvalidFormat, len(frame.Payload), audiosocket.MaxPayloadSize)
	}
	return nil
}

func validateVariableFrame(frame audiosocket.Frame, want audiosocket.FrameType) error {
	return validateFrame(frame, want)
}

func (s *mediaSession) enqueuePCM(ctx context.Context, payload []byte, degraded ...bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.txMu.Lock()
	defer s.txMu.Unlock()
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}
	combinedLen := s.txUsed + len(payload)
	frameCount := combinedLen / txFrameBytes
	combined := make([]byte, combinedLen)
	copy(combined, s.txPartial[:s.txUsed])
	copy(combined[s.txUsed:], payload)
	for offset := 0; offset < frameCount*txFrameBytes; offset += txFrameBytes {
		frame := txFrame{Frame: audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: append([]byte(nil), combined[offset:offset+txFrameBytes]...)}, degraded: len(degraded) > 0 && degraded[0]}
		if len(s.txQueue) == cap(s.txQueue) {
			started := time.Now()
			s.metrics.txWaitCount.Add(1)
			select {
			case <-s.done:
				s.metrics.txWaitDurationNS.Add(uint64(time.Since(started)))
				return ErrSessionClosed
			case <-ctx.Done():
				s.metrics.txWaitDurationNS.Add(uint64(time.Since(started)))
				return ctx.Err()
			case s.txQueue <- frame:
				s.metrics.txWaitDurationNS.Add(uint64(time.Since(started)))
			}
		} else {
			select {
			case <-s.done:
				return ErrSessionClosed
			case <-ctx.Done():
				return ctx.Err()
			case s.txQueue <- frame:
			}
		}
		updateHighWater(&s.metrics.txQueueHighWater, uint64(len(s.txQueue)))
	}
	remainder := combinedLen - frameCount*txFrameBytes
	clear(s.txPartial[:])
	copy(s.txPartial[:remainder], combined[frameCount*txFrameBytes:])
	s.txUsed = remainder
	return nil
}

func (s *mediaSession) sessionError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (a *Adapter) readRX(s *mediaSession) {
	defer s.wg.Done()
	for {
		frame, err := audiosocket.DecodeReader(s.rxConn)
		if err != nil {
			a.endSession(s, err)
			return
		}
		if err = validateFrame(frame, audiosocket.TypeSlin16); err != nil {
			a.endSession(s, err)
			return
		}
		select {
		case <-s.done:
			return
		case s.rxQueue <- frame:
			updateHighWater(&s.metrics.rxQueueHighWater, uint64(len(s.rxQueue)))
		default:
			// Keep the newest real-time audio and discard exactly one stale
			// queued frame. The queue remains bounded and SIP stays alive.
			select {
			case stale := <-s.rxQueue:
				clear(stale.Payload)
				s.metrics.rxFramesDropped.Add(1)
			default:
			}
			select {
			case <-s.done:
				return
			case s.rxQueue <- frame:
				updateHighWater(&s.metrics.rxQueueHighWater, uint64(len(s.rxQueue)))
			default:
				clear(frame.Payload)
				s.metrics.rxFramesDropped.Add(1)
			}
		}
	}
}

func updateHighWater(value *atomic.Uint64, candidate uint64) {
	for current := value.Load(); candidate > current; current = value.Load() {
		if value.CompareAndSwap(current, candidate) {
			return
		}
	}
}

// Metrics returns a safe snapshot of queue pressure for this call.
func (s *Session) Metrics() SessionMetrics {
	if s == nil || s.state == nil {
		return SessionMetrics{}
	}
	m := &s.state.metrics
	return SessionMetrics{
		RXFramesDropped:       m.rxFramesDropped.Load(),
		RXQueueHighWater:      m.rxQueueHighWater.Load(),
		TXQueueHighWater:      m.txQueueHighWater.Load(),
		TXWaitCount:           m.txWaitCount.Load(),
		TXWaitDurationMS:      m.txWaitDurationNS.Load() / uint64(time.Millisecond),
		RealAgentAudioFrames:  m.realAgentAudioFrames.Load(),
		DegradedSilenceFrames: m.degradedSilenceFrames.Load(),
		LastRealAgentAudioAt:  timestampNS(m.lastRealAudioNS.Load()),
		DegradedModeStartedAt: timestampNS(m.degradedStartedNS.Load()),
	}
}

// WaitClosed waits until the Baresip media peer ends this call or shutdown
// closes the adapter-owned transport.
func (s *Session) WaitClosed(ctx context.Context) error {
	if s == nil || s.state == nil {
		return ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.state.done:
		return s.state.sessionError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsClosed reports whether this call-scoped media transport has terminated.
func (s *Session) IsClosed() bool {
	if s == nil || s.state == nil {
		return true
	}
	select {
	case <-s.state.done:
		return true
	default:
		return false
	}
}

// ServeDegraded drains inbound frames and writes silence at the negotiated
// source cadence until the telephony session closes or runtime shuts down.
func (s *Session) ServeDegraded(ctx context.Context) error {
	if s == nil || s.state == nil {
		return ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.state.metrics.degradedStartedNS.CompareAndSwap(0, time.Now().UTC().UnixNano())
	s.state.txMu.Lock()
	clear(s.state.txPartial[:])
	s.state.txUsed = 0
	s.state.txMu.Unlock()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			frame, err := s.ReadFrameContext(ctx)
			if err != nil {
				return
			}
			clear(frame.Payload)
		}
	}()
	defer func() { cancel(); <-drainDone }()
	ticker := time.NewTicker(ptimeMillis * time.Millisecond)
	defer ticker.Stop()
	silence := make([]byte, txFrameBytes)
	for {
		select {
		case <-s.state.done:
			<-drainDone
			return s.state.sessionError()
		case <-ctx.Done():
			<-drainDone
			return ctx.Err()
		case <-ticker.C:
			if err := s.state.enqueuePCM(ctx, silence, true); err != nil {
				if errors.Is(err, ErrSessionClosed) {
					<-drainDone
					return s.state.sessionError()
				}
				if ctx.Err() != nil {
					<-drainDone
					return ctx.Err()
				}
				return err
			}
		}
	}
}

func (a *Adapter) writeTX(s *mediaSession) {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.txQueue:
			wire, err := audiosocket.Encode(frame.Frame)
			if err == nil {
				err = writeFull(s.txConn, wire)
			}
			if err != nil {
				a.endSession(s, err)
				return
			}
			if frame.degraded {
				s.metrics.degradedSilenceFrames.Add(1)
			} else {
				s.metrics.realAgentAudioFrames.Add(1)
				s.metrics.lastRealAudioNS.Store(time.Now().UTC().UnixNano())
			}
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

func (a *Adapter) endSession(s *mediaSession, err error) {
	s.endOnce.Do(func() {
		// The session workers are still counted until their deferred Done calls,
		// so registering this cleanup before waking them is safe alongside Close.
		a.wg.Add(1)
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
		_ = s.rxConn.Close()
		_ = s.txConn.Close()

		// Cleanup runs after both session workers have exited. Every call owns
		// separate queues, and the next pair is not promoted until cleanup has
		// cleared all old frames and partial PCM.
		go func() {
			defer a.wg.Done()
			s.wg.Wait()
			s.txMu.Lock()
			clear(s.txPartial[:])
			s.txUsed = 0
			s.txMu.Unlock()
			drainFrames(s.rxQueue)
			for len(s.txQueue) > 0 {
				frame := <-s.txQueue
				clear(frame.Payload)
			}
			a.mu.Lock()
			if a.active == s {
				a.active = nil
			}
			var next *mediaSession
			if !a.closed && a.active == nil && a.rxPending != nil && a.txPending != nil {
				next = a.activatePendingLocked()
			} else if a.active == nil {
				a.notifyLocked()
			}
			a.mu.Unlock()
			if next != nil {
				a.startSession(next)
			}
		}()
	})
}

func drainFrames(frames chan audiosocket.Frame) {
	for {
		select {
		case frame := <-frames:
			clear(frame.Payload)
		default:
			return
		}
	}
}

func isNormalDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, audiosocket.ErrInvalidHeader) || errors.Is(err, audiosocket.ErrTruncatedPayload)
}

func (a *Adapter) stop(err error) {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		if err == nil {
			err = ErrClosed
		}
		a.err = err
		rxPending, txPending, active := a.rxPending, a.txPending, a.active
		a.rxPending, a.txPending = nil, nil
		if a.pairTimer != nil {
			a.pairTimer.Stop()
			a.pairTimer = nil
		}
		a.pairEpoch++
		a.notifyLocked()
		a.mu.Unlock()
		a.cancel()
		if a.rxListen != nil {
			_ = a.rxListen.Close()
		}
		if a.txListen != nil {
			_ = a.txListen.Close()
		}
		if rxPending != nil {
			_ = rxPending.Close()
		}
		if txPending != nil {
			_ = txPending.Close()
		}
		if active != nil {
			a.endSession(active, err)
		}
		_ = os.Remove(a.rxPath)
		_ = os.Remove(a.txPath)
		_ = os.Remove(a.dir)
		close(a.done)
	})
}

func (a *Adapter) terminalError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminalErrorLocked()
}

func (a *Adapter) terminalErrorLocked() error {
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

func timestampNS(ns int64) *time.Time {
	if ns == 0 {
		return nil
	}
	t := time.Unix(0, ns).UTC()
	return &t
}
