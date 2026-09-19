package audiosocket

import (
	"context"
	"errors"
	"net"
	"sync"
)

var (
	// ErrNilHandler indicates that a server was created without a connection handler.
	ErrNilHandler = errors.New("audiosocket: nil connection handler")
	// ErrServerRunning indicates that Serve was called more than once concurrently.
	ErrServerRunning = errors.New("audiosocket: server already running")
	// ErrServerClosed indicates that the server lifecycle has been terminated.
	ErrServerClosed = errors.New("audiosocket: server closed")
)

// ConnectionHandler handles one accepted AudioSocket connection.
type ConnectionHandler func(context.Context, *Stream) error

// Server owns the TCP listener and the lifecycle of accepted AudioSocket
// connections. It does not impose a concurrency limit.
type Server struct {
	address string
	handler ConnectionHandler

	mu        sync.Mutex
	listener  net.Listener
	active    net.Conn
	serveDone chan struct{}
	serving   bool
	shutdown  bool
	closed    bool
}

// NewServer creates a TCP server using the caller-provided address.
func NewServer(address string, handler ConnectionHandler) *Server {
	return &Server{address: address, handler: handler}
}

// Listen binds the configured address. It does not accept connections.
func (s *Server) Listen() error {
	if s == nil {
		return net.ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrServerClosed
	}
	if s.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return err
	}
	s.listener = listener
	return nil
}

// Addr returns the bound listener address, or nil before Listen succeeds.
func (s *Server) Addr() net.Addr {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Serve accepts connections until the context is canceled, the listener
// fails, or a connection handler returns an error. Context cancellation is a
// clean shutdown and returns nil.
func (s *Server) Serve(ctx context.Context) error {
	if s == nil {
		return net.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.handler == nil {
		return ErrNilHandler
	}
	if err := s.Listen(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrServerClosed
	}
	if s.serving {
		s.mu.Unlock()
		return ErrServerRunning
	}
	s.serving = true
	s.serveDone = make(chan struct{})
	listener := s.listener
	done := s.serveDone
	s.mu.Unlock()

	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.requestShutdown()
		case <-watcherDone:
		}
	}()
	defer close(watcherDone)
	defer func() {
		s.mu.Lock()
		s.serving = false
		close(done)
		s.mu.Unlock()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || s.isShutdown() {
				return nil
			}
			return err
		}

		s.mu.Lock()
		if s.shutdown || s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		s.active = conn
		s.mu.Unlock()
		err = s.handler(ctx, NewStream(conn, conn))
		_ = conn.Close()
		s.mu.Lock()
		s.active = nil
		shutdown := s.shutdown
		s.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil || shutdown {
				return nil
			}
			s.requestShutdown()
			return err
		}
		if ctx.Err() != nil || shutdown {
			return nil
		}
	}
}

// Shutdown stops accepting connections, closes the active connection to
// unblock a handler, and waits for Serve to return or the context to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.requestShutdown()
	s.mu.Lock()
	done := s.serveDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close requests shutdown without waiting for active handlers.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.requestShutdown()
	return nil
}

func (s *Server) requestShutdown() {
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		return
	}
	s.shutdown = true
	s.closed = true
	listener := s.listener
	active := s.active
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if active != nil {
		_ = active.Close()
	}
}

func (s *Server) isShutdown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdown
}
