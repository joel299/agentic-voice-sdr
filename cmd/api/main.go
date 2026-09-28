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
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/voiceflow"
)

type configLoader func() (config.Config, error)
type serverRunner func(context.Context, config.Config) error
type serveFunc func() error

type falePacoServer interface {
	Listen() error
	Serve(context.Context) error
	Shutdown(context.Context) error
}

var productionFalePacoRuntime = func(addr string, geminiConfig geminilive.Config, logger voiceflow.FalePacoLogger) (falePacoServer, error) {
	return voiceflow.NewProductionFalePacoRuntimeWithLogger(addr, geminiConfig, logger)
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
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
	server := newHTTPServer(cfg.HTTPAddr, httpapi.NewRouterWithConfig(cfg))
	server.ReadTimeout = cfg.ReadTimeout
	server.WriteTimeout = cfg.WriteTimeout
	server.IdleTimeout = cfg.IdleTimeout
	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var audio falePacoServer
	var logger voiceflow.FalePacoLogger
	if cfg.FalePacoAudioSocketEnabled {
		var err error
		logger, err = voiceflow.NewFalePacoFileLogger(cfg.FalePacoRuntimeLogPath)
		if err != nil {
			return fmt.Errorf("create Fale Paco runtime logger: %w", err)
		}
		audio, err = productionFalePacoRuntime(cfg.FalePacoAudioSocketAddr, geminilive.ConfigFromEnv(), logger)
		if err != nil {
			_ = logger.Close()
			return fmt.Errorf("configure Fale Paco runtime: %w", err)
		}
		if err = audio.Listen(); err != nil {
			_ = logger.Close()
			return fmt.Errorf("bind Fale Paco AudioSocket %s: %w", cfg.FalePacoAudioSocketAddr, err)
		}
	}
	return runServers(signalCtx, server, audio, cfg.ShutdownGrace, server.ListenAndServe)
}

func runServers(ctx context.Context, server *http.Server, audio falePacoServer, grace time.Duration, httpServe serveFunc) error {
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	httpErr := make(chan error, 1)
	go func() { httpErr <- httpServe() }()
	var audioErr <-chan error
	if audio != nil {
		ch := make(chan error, 1)
		audioErr = ch
		go func() { ch <- audio.Serve(root) }()
	}
	select {
	case err := <-httpErr:
		cancel()
		if audio != nil {
			_ = audio.Shutdown(context.Background())
		}
		return normalizeServeError(err)
	case err := <-audioErr:
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), grace)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return err
		}
		return nil
	case <-root.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), grace)
		defer stop()
		var audioShutdown error
		if audio != nil {
			audioShutdown = audio.Shutdown(shutdownCtx)
		}
		httpShutdown := server.Shutdown(shutdownCtx)
		httpServeErr := <-httpErr
		if audio != nil {
			<-audioErr
		}
		if audioShutdown != nil {
			return fmt.Errorf("Fale Paco shutdown: %w", audioShutdown)
		}
		if httpShutdown != nil {
			return fmt.Errorf("HTTP shutdown: %w", httpShutdown)
		}
		return normalizeServeError(httpServeErr)
	}
}

func runServer(ctx context.Context, server *http.Server, shutdownGrace time.Duration, serve serveFunc) error {
	return runServers(ctx, server, nil, shutdownGrace, serve)
}
func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
