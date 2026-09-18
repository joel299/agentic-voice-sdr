package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
)

type configLoader func() (config.Config, error)
type serverRunner func(context.Context, config.Config) error

type serveFunc func() error

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

func main() {
	if err := run(context.Background(), config.Load, serve); err != nil {
		log.Printf("API startup error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, load configLoader, serve serverRunner) error {
	cfg, err := load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	return serve(ctx, cfg)
}

func serve(ctx context.Context, cfg config.Config) error {
	server := newHTTPServer(cfg.HTTPAddr, httpapi.NewRouter())
	server.ReadTimeout = cfg.ReadTimeout
	server.WriteTimeout = cfg.WriteTimeout
	server.IdleTimeout = cfg.IdleTimeout

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runServer(signalCtx, server, cfg.ShutdownGrace, server.ListenAndServe)
}

func runServer(ctx context.Context, server *http.Server, shutdownGrace time.Duration, serve serveFunc) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serve()
	}()

	select {
	case err := <-serveErr:
		return normalizeServeError(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()

		serveErrValue := <-serveErr
		if shutdownErr != nil {
			return fmt.Errorf("graceful shutdown: %w", shutdownErr)
		}
		return normalizeServeError(serveErrValue)
	}
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
