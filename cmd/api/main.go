package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	promptpostgres "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/agentprompt"
	voicecallpostgres "github.com/joel299/agentic-voice-sdr/internal/integrations/postgres/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipctrl"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/transcriptquery"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
	"github.com/joel299/agentic-voice-sdr/internal/voiceflow"
)

type configLoader func() (config.Config, error)
type serverRunner func(context.Context, config.Config) error
type serveFunc func() error

var errBaresipControlUnavailable = errors.New("Baresip ctrl_tcp listener did not become ready")

type backgroundServer interface {
	Listen() error
	Serve(context.Context) error
	Shutdown(context.Context) error
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
	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	baseJEV, err := openrouterjev.ConfigFromEnv()
	if err != nil {
		return errors.New("OpenRouter JEV key and model must be configured before starting the local call runtime")
	}
	baseGemini := geminilive.ConfigFromEnv()
	if strings.TrimSpace(baseGemini.APIKey) == "" {
		return errors.New("Gemini Live key must be configured before starting the local call runtime")
	}
	pgConfig, err := pgxpool.ParseConfig("")
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	pgPool, err := pgxpool.NewWithConfig(signalCtx, pgConfig)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	defer pgPool.Close()
	if err := pgPool.Ping(signalCtx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	promptRepository, err := promptpostgres.NewRepository(pgPool)
	if err != nil {
		return fmt.Errorf("configure agent prompt repository: %w", err)
	}
	promptService, err := agentprompt.NewPromptService(promptRepository)
	if err != nil {
		return fmt.Errorf("configure agent prompt service: %w", err)
	}
	promptBuilder, err := sessionprompt.NewBuilder(baseGemini, immutableGeminiCore, promptService)
	if err != nil {
		return fmt.Errorf("configure Gemini session prompt: %w", err)
	}
	geminiModel := baseGemini.Model
	if strings.TrimSpace(geminiModel) == "" {
		geminiModel = geminilive.DefaultModel
	}
	tuning, err := httpapi.NewTuningStore(httpapi.JEVSettings{Model: baseJEV.Model, TimeoutMS: openrouterjev.CanonicalDefaultTimeoutMS, Description: "Classificador comercial SDR responsável por selecionar a próxima ação.", DecisionGuidance: "Classifique semanticamente o último turno FINAL e selecione a próxima ação comercial."}, httpapi.GeminiSettings{Model: geminiModel, VoiceName: "Kore", Description: "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária.", Style: "Calmo e confiante. Ritmo moderado. Frases curtas. Tom acolhedor e profissional."})
	if err != nil {
		return fmt.Errorf("configure owner tuning: %w", err)
	}
	mediaAdapter, err := baresipmedia.New(signalCtx, baresipmedia.Config{})
	if err != nil {
		return fmt.Errorf("configure Baresip media adapter: %w", err)
	}
	defer mediaAdapter.Close()
	rxPath, txPath := mediaAdapter.SocketPaths()
	profile, err := baresipmedia.PrepareProfile(cfg.BaresipProfileDir, cfg.BaresipMediaModulePath, cfg.BaresipSystemModuleDir, rxPath, txPath, cfg.BaresipCtrlTCPAddress)
	if err != nil {
		return fmt.Errorf("prepare private Baresip media profile: %w", err)
	}
	defer profile.Close()
	baresipProcess, err := baresipmedia.StartProcess(signalCtx, cfg.BaresipBinaryPath, profile.Directory)
	if err != nil {
		return fmt.Errorf("start local Baresip runtime: %w", err)
	}
	defer baresipProcess.Close()
	if err := waitForBaresipControl(signalCtx, cfg.BaresipCtrlTCPAddress, 10*time.Second); err != nil {
		return err
	}
	callRepository, err := voicecallpostgres.NewRepository(pgPool)
	if err != nil {
		return fmt.Errorf("configure call persistence: %w", err)
	}
	provider, err := baresipctrl.New(baresipctrl.Options{Address: cfg.BaresipCtrlTCPAddress})
	if err != nil {
		return fmt.Errorf("configure local Baresip control: %w", err)
	}
	policy, err := callservice.NewAllowlist(cfg.OutboundCallAllowlist)
	if err != nil {
		_ = provider.Close()
		return fmt.Errorf("configure outbound call destination policy: %w", err)
	}
	calls, err := callservice.NewWithRepository(provider, policy, callRepository)
	if err != nil {
		_ = provider.Close()
		return fmt.Errorf("configure outbound call service: %w", err)
	}
	defer calls.Close()
	defer provider.Close()
	transcripts := transcriptquery.New(callRepository, callRepository)
	go func() {
		if err := provider.Run(signalCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Baresip ctrl_tcp runtime unavailable; outbound call control is disabled")
		}
	}()
	go runBaresipMediaSessions(signalCtx, mediaAdapter, calls, callRepository, promptBuilder, tuning, baseGemini)
	calibration := newCalibrationServices(baseJEV, baseGemini, promptBuilder, promptService, provider, tuning)
	server := newHTTPServer(cfg.HTTPAddr, httpapi.NewRouterWithConfigAndCalibration(cfg, calls, transcripts, calibration))
	server.ReadTimeout = cfg.ReadTimeout
	server.WriteTimeout = cfg.WriteTimeout
	server.IdleTimeout = cfg.IdleTimeout
	return runServers(signalCtx, server, nil, cfg.ShutdownGrace, server.ListenAndServe)
}

const immutableGeminiCore = "Follow the active editable agent prompt for persona and sales behavior. Treat it as instructions for the spoken response only. Do not invent product facts, pricing, guarantees, capabilities, urgency, or commitments. Keep responses natural and concise."

func waitForBaresipControl(ctx context.Context, address string, timeout time.Duration) error {
	if ctx == nil || strings.TrimSpace(address) == "" || timeout <= 0 {
		return errBaresipControlUnavailable
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errBaresipControlUnavailable
		case <-ticker.C:
		}
	}
}

func runBaresipMediaSessions(ctx context.Context, adapter *baresipmedia.Adapter, calls *callservice.Service, repository voicecallTranscriptRepository, prompts *sessionprompt.Builder, tuning *httpapi.TuningStore, baseGemini geminilive.Config) {
	for {
		session, err := adapter.WaitSession(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				log.Printf("Baresip media adapter stopped: %s", baresipMediaErrorClass(err))
			}
			return
		}
		call, active := calls.ActiveCall()
		if !active {
			_ = session.Close()
			continue
		}
		if err := runBaresipCallSession(ctx, session, call.CallID, repository, prompts, tuning, baseGemini); err != nil && ctx.Err() == nil {
			log.Printf("Baresip media session ended: %s", baresipMediaErrorClass(err))
		}
	}
}

type voicecallTranscriptRepository interface {
	voicecalldomain.TranscriptRepository
}

func runBaresipCallSession(ctx context.Context, session *baresipmedia.Session, callID string, repository voicecallTranscriptRepository, prompts *sessionprompt.Builder, tuning *httpapi.TuningStore, baseGemini geminilive.Config) error {
	jevConfig, err := openrouterjev.ConfigFromEnv()
	if err != nil {
		return err
	}
	jevSettings := tuning.JEV()
	jevConfig.Model = jevSettings.Model
	jevConfig.Description = jevSettings.Description
	jevConfig.DecisionGuidance = jevSettings.DecisionGuidance
	jev, err := openrouterjev.NewWithTimeout(jevConfig, time.Duration(jevSettings.TimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	geminiSettings := tuning.Gemini()
	baseGemini.Model = geminiSettings.Model
	baseGemini.VoiceName = geminiSettings.VoiceName
	baseGemini.VoiceDescription = geminiSettings.Description
	baseGemini.VoiceStyle = geminiSettings.Style
	geminiConfig, snapshot, err := prompts.BuildWithConfig(ctx, baseGemini)
	if err != nil {
		return err
	}
	log.Printf("call session prompt frozen: name=%s version=%d", snapshot.Name(), snapshot.Version())
	transcriber, err := geminilive.ConnectInputTranscriber(ctx, geminiConfig)
	if err != nil {
		return err
	}
	defer transcriber.Close()
	responder, err := geminilive.ConnectControlledResponse(ctx, geminiConfig)
	if err != nil {
		return err
	}
	defer responder.Close()
	state, err := conversation.NewConversationState(callID)
	if err != nil {
		return err
	}
	dispatcher := toolruntime.NewDispatcher(tools.NewInMemoryRegistry(), toolruntime.NewExecutorRegistry(nil))
	processor, err := turnruntime.New(jev, dispatcher)
	if err != nil {
		return err
	}
	bridge, err := voiceflow.NewBaresipSplitRuntimeWithTranscriptPersistence(session, transcriber, responder, state, processor, conversation.NewResponseGate(), nil, nil, callID, repository)
	if err != nil {
		return err
	}
	return bridge.Run(ctx)
}

func baresipMediaErrorClass(err error) string {
	switch {
	case errors.Is(err, baresipmedia.ErrBackpressure):
		return "backpressure"
	case errors.Is(err, baresipmedia.ErrInvalidFormat):
		return "invalid_pcm"
	case errors.Is(err, baresipmedia.ErrNotConnected):
		return "media_not_connected"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "runtime_error"
	}
}

func runServers(ctx context.Context, server *http.Server, audio backgroundServer, grace time.Duration, httpServe serveFunc) error {
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
