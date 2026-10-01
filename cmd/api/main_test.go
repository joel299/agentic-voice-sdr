package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

func TestServerConfiguresExplicitTimeouts(t *testing.T) {
	server := newHTTPServer(":0", nil)
	if server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatal("HTTP server timeouts must be positive")
	}
}

func TestWaitForBaresipControlRequiresListeningEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForBaresipControl(ctx, listener.Addr().String(), time.Second); err != nil {
		t.Fatalf("ready local control listener rejected: %v", err)
	}

	if err := waitForBaresipControl(ctx, "127.0.0.1:1", 150*time.Millisecond); !errors.Is(err, errBaresipControlUnavailable) {
		t.Fatalf("missing control listener error = %v", err)
	}
}

func TestGracefulShutdownWaitsForActiveRequests(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := newHTTPServer(":0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- runServer(ctx, server, 1*time.Second, func() error {
			return server.Serve(listener)
		})
	}()

	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr != nil {
			requestDone <- requestErr
			return
		}
		response.Body.Close()
		requestDone <- nil
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}

	cancel()
	select {
	case err := <-runDone:
		t.Fatalf("runServer returned before active request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRequest)
	if err := <-requestDone; err != nil {
		t.Fatalf("request error = %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("runServer() error = %v", err)
	}
}

func TestMainExitsNonZeroOnInvalidConfiguration(t *testing.T) {
	if os.Getenv("GRU59_INVALID_CONFIG_CHILD") == "1" {
		os.Setenv("HTTP_READ_TIMEOUT", "not-a-duration")
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsNonZeroOnInvalidConfiguration")
	cmd.Env = append(os.Environ(), "GRU59_INVALID_CONFIG_CHILD=1")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child process error = %v, want non-zero exit", err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatal("invalid configuration exited with code 0")
	}
}

func TestRunReturnsConfigurationError(t *testing.T) {
	wantErr := errors.New("invalid configuration")
	serveCalled := false
	err := run(context.Background(), func() (config.Config, error) {
		return config.Config{}, wantErr
	}, func(context.Context, config.Config) error {
		serveCalled = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("run() error = %v, want %v", err, wantErr)
	}
	if serveCalled {
		t.Fatal("server runner called after configuration failure")
	}
}

func TestRuntimeCompositionUsesPersistentWhatsAppStore(t *testing.T) {
	path := t.TempDir() + "/whatsapp.json"
	store := &whatsapp.FileConfigStore{Path: path}
	if err := store.Save(context.Background(), whatsapp.ConfigMetadata{Provider: "test", BaseURL: "https://provider.example", ActiveInstanceID: "wa-1", ProviderStatus: whatsapp.StatusReady}); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewRouterWithConfig(config.Config{WhatsAppConfigPath: path})
	request := httptest.NewRequest(http.MethodGet, "/v1/config/whatsapp", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active_instance_id":"wa-1"`) || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("persistent runtime store not wired: status=%d body=%s", response.Code, response.Body)
	}
}

type testAudioServer struct {
	serveStarted   chan struct{}
	shutdownCalled chan struct{}
	once           sync.Once
}

func (s *testAudioServer) Listen() error { return nil }
func (s *testAudioServer) Serve(ctx context.Context) error {
	close(s.serveStarted)
	<-ctx.Done()
	return nil
}
func (s *testAudioServer) Shutdown(context.Context) error {
	s.once.Do(func() { close(s.shutdownCalled) })
	return nil
}

func TestRunServersStartsAndShutsDownHTTPAndAudioSocket(t *testing.T) {
	audio := &testAudioServer{serveStarted: make(chan struct{}), shutdownCalled: make(chan struct{})}
	httpDone := make(chan struct{})
	server := newHTTPServer(":0", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runServers(ctx, server, audio, time.Second, func() error { <-httpDone; return http.ErrServerClosed })
	}()
	select {
	case <-audio.serveStarted:
	case <-time.After(time.Second):
		t.Fatal("AudioSocket runtime did not start")
	}
	cancel()
	close(httpDone)
	select {
	case <-audio.shutdownCalled:
	case <-time.After(time.Second):
		t.Fatal("AudioSocket runtime did not shut down")
	}
	if err := <-done; err != nil {
		t.Fatalf("runServers() error = %v", err)
	}
}
