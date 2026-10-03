package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipctrl"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	telephonybridge "github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
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
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || (os.Args[1] != "--local-env" && os.Args[1] != "--check-local-env") {
			log.Print("invalid local launch arguments")
			os.Exit(2)
		}
		if err := config.LoadLocalEnv(os.Args[2]); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		if err := config.ValidateLocalRuntime(os.Stdout); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		if os.Args[1] == "--check-local-env" {
			return
		}
		// Re-exec with the exported file environment so /proc reports the same
		// startup configuration that Config.Load and the call policy actually use.
		executable, err := os.Executable()
		if err != nil {
			log.Print("cannot resolve API binary")
			os.Exit(1)
		}
		if err := syscall.Exec(executable, []string{executable}, os.Environ()); err != nil {
			log.Print("cannot exec configured API binary")
			os.Exit(1)
		}

	}

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
	tuning, err := httpapi.NewTuningStore(httpapi.JEVSettings{Model: baseJEV.Model, TimeoutMS: openrouterjev.CanonicalDefaultTimeoutMS, Description: "Classificador comercial SDR responsável por selecionar a próxima ação.", DecisionGuidance: "Classifique semanticamente o último turno FINAL e selecione a próxima ação comercial."}, httpapi.GeminiSettings{Model: geminiModel, VoiceName: "Fola", Description: "Voz comercial brasileira, humana e consultiva. Deve transmitir clareza, proximidade e confiança sem parecer locução publicitária.", Style: "PT-BR natural e consultivo. Responda em uma ou duas frases curtas, com uma pergunta por vez. <breath> indica uma respiração discreta numa pausa natural; não pronuncie a marcação nem acrescente pausas longas."})
	if err != nil {
		return fmt.Errorf("configure owner tuning: %w", err)
	}
	mediaAdapter, err := baresipmedia.New(signalCtx, localBaresipMediaConfig())
	if err != nil {
		return fmt.Errorf("configure Baresip media adapter: %w", err)
	}
	defer mediaAdapter.Close()
	rxPath, txPath := mediaAdapter.SocketPaths()
	baresipRuntime, err := startBaresipRuntime(signalCtx, cfg, rxPath, txPath)
	if err != nil {
		return fmt.Errorf("start local Baresip runtime: %w", err)
	}
	defer baresipRuntime.Close()
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
	sourceBaseline, sourceBaselineOK := readMediaSourceStats(signalCtx, provider)
	go runBaresipMediaSessions(signalCtx, mediaAdapter, calls, callRepository, promptBuilder, tuning, baseGemini, provider, sourceBaseline, sourceBaselineOK)
	calibration := newCalibrationServices(baseJEV, baseGemini, promptBuilder, promptService, provider, tuning)
	calibration.FalePacoSIP = newFalePacoSIPService(cfg.BaresipProfileDir, provider)
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

func runBaresipMediaSessions(ctx context.Context, adapter *baresipmedia.Adapter, calls *callservice.Service, repository voicecallTranscriptRepository, prompts *sessionprompt.Builder, tuning *httpapi.TuningStore, baseGemini geminilive.Config, sourceStats *baresipctrl.Client, baseline baresipctrl.MediaSourceStats, baselineOK bool) {
	// Snapshot once before calls can allocate audio, then after each complete
	// single-call interval. A failed baseline remains unavailable, not zero.
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
			log.Printf("media_session_close call_id=none reason=no_active_call")
			_ = session.Close()
			continue
		}
		openedAt := time.Now().UTC()
		log.Printf("media_session_open api_call_id=%s at=%s", call.CallID, openedAt.Format(time.RFC3339Nano))
		recordAIRuntimeStatus(ctx, calls, call.CallID, "starting", "runtime_starting", "", nil)
		callCtx, cancelCall := context.WithCancel(ctx)
		go func() {
			if err := calls.WaitCallEnd(callCtx, call.CallID); err == nil {
				cancelCall()
			}
		}()
		var runtimeReady atomic.Bool
		var milestoneMu sync.Mutex
		var frameMu sync.Mutex
		seenFrames := make(map[string]bool)
		observe := func(stage, outcome string) {
			if !safeStageToken(stage) || !safeStageToken(outcome) {
				return
			}
			// Frame observations never wait behind a PostgreSQL stage update.
			if frameMilestone(stage) {
				frameMu.Lock()
				seen := seenFrames[stage]
				seenFrames[stage] = true
				frameMu.Unlock()
				if !seen {
					calls.RecordMediaMilestone(call.CallID, stage)
					log.Printf("ai_runtime_milestone api_call_id=%s stage=%s outcome=%s at=%s", call.CallID, stage, outcome, time.Now().UTC().Format(time.RFC3339Nano))
				}
				return
			}
			milestoneMu.Lock()
			defer milestoneMu.Unlock()
			calls.RecordMediaMilestone(call.CallID, stage)
			at := time.Now().UTC()
			category := stageCategory(stage)
			status := "starting"
			if runtimeReady.Load() || (stage == "bridge_run" && outcome == "started") {
				runtimeReady.Store(true)
				status = "running"
			}
			recordAIRuntimeStatus(callCtx, calls, call.CallID, status, category, "", nil)
			log.Printf("ai_runtime_milestone api_call_id=%s stage=%s outcome=%s at=%s", call.CallID, stage, outcome, at.Format(time.RFC3339Nano))
		}
		aiErr := runBaresipCallSession(callCtx, session, calls, call.CallID, repository, prompts, tuning, baseGemini, observe)
		currentCall, stillActive := calls.ActiveCall()
		if ctx.Err() == nil && (!stillActive || currentCall.CallID != call.CallID) {
			// CallService only clears the active call for a real terminal Baresip
			// event or an owner Hangup reconciliation. That is an allowed owner
			// of media teardown.
			cancelCall()
			recordAIRuntimeStopped(ctx, calls, call.CallID)
			_ = session.Close()
			metrics := session.Metrics()
			logSafeMediaMetrics(call.CallID, metrics)
			baseline, baselineOK = finishSourceStats(ctx, sourceStats, calls, call.CallID, baseline, baselineOK)
			log.Printf("media_session_close api_call_id=%s reason=provider_terminal at=%s rx_frames_dropped=%d rx_queue_high_water=%d tx_queue_high_water=%d tx_wait_count=%d tx_wait_duration_ms=%d", call.CallID, time.Now().UTC().Format(time.RFC3339Nano), metrics.RXFramesDropped, metrics.RXQueueHighWater, metrics.TXQueueHighWater, metrics.TXWaitCount, metrics.TXWaitDurationMS)
			continue
		}
		failureStage := aiFailureStage(aiErr)
		failureClass := aiFailureClass(aiErr, failureStage)
		var mediaErr error
		if ctx.Err() == nil {
			failedAt := time.Now().UTC()
			recordAIRuntimeStatus(ctx, calls, call.CallID, "failed", failureStage, failureClass, &failedAt)
			logSafeMediaMetrics(call.CallID, session.Metrics())
			log.Printf("ai_runtime_status=failed api_call_id=%s ai_runtime_stage=%s ai_failure_class=%s ai_failure_at=%s media_mode=degraded degraded_reason=%s", call.CallID, failureStage, failureClass, failedAt.Format(time.RFC3339Nano), failureClass)
			log.Printf("ai_runtime_milestone api_call_id=%s stage=degraded_mode outcome=started at=%s", call.CallID, failedAt.Format(time.RFC3339Nano))
			mediaErr = session.ServeDegraded(callCtx)
		}
		currentCall, stillActive = calls.ActiveCall()
		terminalCall := !stillActive || currentCall.CallID != call.CallID
		cancelCall()
		metrics := session.Metrics()
		logSafeMediaMetrics(call.CallID, metrics)
		baseline, baselineOK = finishSourceStats(ctx, sourceStats, calls, call.CallID, baseline, baselineOK)
		if ctx.Err() != nil {
			recordAIRuntimeStopped(context.Background(), calls, call.CallID)
			_ = session.Close()
			log.Printf("media_session_close api_call_id=%s reason=runtime_shutdown at=%s rx_frames_dropped=%d rx_queue_high_water=%d tx_queue_high_water=%d tx_wait_count=%d tx_wait_duration_ms=%d", call.CallID, time.Now().UTC().Format(time.RFC3339Nano), metrics.RXFramesDropped, metrics.RXQueueHighWater, metrics.TXQueueHighWater, metrics.TXWaitCount, metrics.TXWaitDurationMS)
		} else if terminalCall {
			recordAIRuntimeStopped(ctx, calls, call.CallID)
			_ = session.Close()
			log.Printf("media_session_close api_call_id=%s reason=provider_terminal at=%s rx_frames_dropped=%d rx_queue_high_water=%d tx_queue_high_water=%d tx_wait_count=%d tx_wait_duration_ms=%d", call.CallID, time.Now().UTC().Format(time.RFC3339Nano), metrics.RXFramesDropped, metrics.RXQueueHighWater, metrics.TXQueueHighWater, metrics.TXWaitCount, metrics.TXWaitDurationMS)
		} else {
			metrics = session.Metrics()
			reason := mediaCloseReason(mediaErr)
			log.Printf("media_session_close api_call_id=%s reason=%s at=%s rx_frames_dropped=%d rx_queue_high_water=%d tx_queue_high_water=%d tx_wait_count=%d tx_wait_duration_ms=%d", call.CallID, reason, time.Now().UTC().Format(time.RFC3339Nano), metrics.RXFramesDropped, metrics.RXQueueHighWater, metrics.TXQueueHighWater, metrics.TXWaitCount, metrics.TXWaitDurationMS)
		}
	}
}

type voicecallTranscriptRepository interface {
	voicecalldomain.TranscriptRepository
}

func runBaresipCallSession(ctx context.Context, session *baresipmedia.Session, calls *callservice.Service, callID string, repository voicecallTranscriptRepository, prompts *sessionprompt.Builder, tuning *httpapi.TuningStore, baseGemini geminilive.Config, observe telephonybridge.StageObserver) error {
	failAtStage := func(stage string, err error) error {
		if err == nil {
			return nil
		}
		if observe != nil {
			observe(stage, "failed")
		}
		return &telephonybridge.StageError{Stage: stage, Cause: err}
	}
	if observe != nil {
		observe("jev_config", "started")
	}
	jevConfig, err := openrouterjev.ConfigFromEnv()
	if err != nil {
		return failAtStage("jev_config", err)
	}
	jevSettings := tuning.JEV()
	jevConfig.Model = jevSettings.Model
	jevConfig.Description = jevSettings.Description
	jevConfig.DecisionGuidance = jevSettings.DecisionGuidance
	jev, err := openrouterjev.NewWithTimeout(jevConfig, time.Duration(jevSettings.TimeoutMS)*time.Millisecond)
	if err != nil {
		return failAtStage("jev_client_init", err)
	}
	if observe != nil {
		observe("jev_client_init", "ready")
	}
	geminiSettings := tuning.Gemini()
	baseGemini.Model = geminiSettings.Model
	baseGemini.VoiceName = geminiSettings.VoiceName
	baseGemini.VoiceDescription = geminiSettings.Description
	baseGemini.VoiceStyle = geminiSettings.Style
	if observe != nil {
		observe("prompt_snapshot", "started")
	}
	geminiConfig, snapshot, err := prompts.BuildWithConfig(ctx, baseGemini)
	if err != nil {
		return failAtStage("prompt_snapshot", err)
	}
	log.Printf("call session prompt frozen: name=%s version=%d", snapshot.Name(), snapshot.Version())
	if observe != nil {
		observe("prompt_snapshot", "ready")
	}
	if observe != nil {
		observe("gemini_input_connect", "started")
	}
	transcriber, err := geminilive.ConnectInputTranscriber(ctx, geminiConfig)
	if err != nil {
		return failAtStage("gemini_input_connect", err)
	}
	defer transcriber.Close()
	if observe != nil {
		observe("gemini_input_connect", "ready")
	}
	if observe != nil {
		observe("gemini_response_connect", "started")
	}
	responder, err := geminilive.ConnectControlledResponse(ctx, geminiConfig)
	if err != nil {
		return failAtStage("gemini_response_connect", err)
	}
	defer responder.Close()
	if observe != nil {
		observe("gemini_response_connect", "ready")
	}
	state, err := conversation.NewConversationState(callID)
	if err != nil {
		return failAtStage("conversation_state", err)
	}
	dispatcher := toolruntime.NewDispatcher(tools.NewInMemoryRegistry(), toolruntime.NewExecutorRegistry(nil))
	if observe != nil {
		observe("turn_runtime_init", "started")
	}
	processor, err := turnruntime.New(jev, dispatcher)
	if err != nil {
		return failAtStage("turn_runtime_init", err)
	}
	if observe != nil {
		processor.SetStageObserver(observe)
	}
	if observe != nil {
		observe("bridge_init", "started")
	}
	bridge, err := voiceflow.NewBaresipSplitRuntimeWithTranscriptPersistence(session, transcriber, responder, state, processor, conversation.NewResponseGate(), nil, nil, callID, repository)
	if err != nil {
		return failAtStage("bridge_init", err)
	}
	if observe != nil {
		observe("bridge_init", "ready")
		bridge.SetStageObserver(observe)
		observe("bridge_run", "started")
	}
	log.Printf("ai_runtime_status=running api_call_id=%s ai_runtime_stage=bridge_run at=%s", callID, time.Now().UTC().Format(time.RFC3339Nano))
	recordAIRuntimeStatus(ctx, calls, callID, "running", "bridge_run", "", nil)
	recording, recordingErr := ownerTestRecording(ctx, calls, callID)
	if recordingErr != nil {
		return failAtStage("bridge_init", recordingErr)
	}
	defer recording.Close()
	bridge.SetOwnerRecording(recording)
	timings := telemetry.NewTurnCollector(256)
	bridge.SetTurnCollector(timings)
	err = bridge.Run(ctx)
	for _, t := range timings.Snapshot() {
		if data, e := json.Marshal(struct {
			telemetry.TurnSnapshot
			Metrics map[string]float64 `json:"metrics_ms"`
		}{t, t.Metrics()}); e == nil {
			log.Printf("ai_turn_timing api_call_id=%s data=%s", callID, data)
		}
	}
	log.Printf("ai_turn_timing_dropped api_call_id=%s count=%d", callID, timings.Dropped())
	if data, e := json.Marshal(bridge.Diagnostics()); e == nil {
		log.Printf("ai_response_diagnostics api_call_id=%s data=%s", callID, data)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A Gemini/AI session can fail independently from a live Baresip call. Keep
	// RX drained and feed bounded silence into the media source until Baresip
	// reports media teardown or the whole runtime is stopped.
	return err
}

func recordAIRuntimeStatus(ctx context.Context, calls *callservice.Service, callID, status, stage, failureClass string, failureAt *time.Time) {
	if err := calls.UpdateAIRuntimeStatus(ctx, callID, status, stage, failureClass, failureAt); err != nil {
		log.Printf("ai_runtime_status_persisted=no api_call_id=%s requested_status=%s error_class=persistence", callID, status)
	}
}

// recordAIRuntimeStopped changes only the runtime status at teardown while
// preserving the last stage that actually ran. Replacing that stage with
// runtime_shutdown hid whether a disconnected call had reached JEV or Gemini.
func recordAIRuntimeStopped(ctx context.Context, calls *callservice.Service, callID string) {
	stage := "runtime_unknown"
	if calls != nil {
		if call, err := calls.Get(callID); err == nil {
			if call.AIFailureAt != nil || call.AIFailureClass != "" || call.AIRuntimeStatus == "failed" || call.AIRuntimeStatus == "degraded" {
				recordAIRuntimeStatus(ctx, calls, callID, "failed", call.AIRuntimeStage, call.AIFailureClass, call.AIFailureAt)
				return
			}
			stage = stoppedAIRuntimeStage(call.AIRuntimeStage)
		}
	}
	recordAIRuntimeStatus(ctx, calls, callID, "stopped", stage, "", nil)
}

func stoppedAIRuntimeStage(lastStage string) string {
	if validAIStage(lastStage) {
		return lastStage
	}
	return "runtime_unknown"
}

func aiFailureStage(err error) string {
	if err == nil {
		return "bridge_run"
	}
	var staged interface{ AIStage() string }
	if errors.As(err, &staged) && validAIStage(staged.AIStage()) {
		return staged.AIStage()
	}
	return "runtime_unknown"
}

func aiFailureClass(err error, stage string) string {
	switch {
	case err == nil:
		return "session_ended"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, telephonybridge.ErrReceiveFailed):
		return "receive_failed"
	case errors.Is(err, telephonybridge.ErrProviderAPI):
		return "provider_api"
	case errors.Is(err, geminilive.ErrTranscriptionAPI):
		return "provider_api"
	case errors.Is(err, geminilive.ErrTranscriptionClosed), errors.Is(err, telephonybridge.ErrResponseTurnIncomplete), errors.Is(err, telephonybridge.ErrSessionClosed):
		return "provider_transport"
	case errors.Is(err, baresipmedia.ErrSessionClosed):
		return "media_closed"
	default:
		var providerErr *geminilive.Error
		if errors.As(err, &providerErr) && (providerErr.Kind == geminilive.ErrorReceive || providerErr.Kind == geminilive.ErrorRemoteClose || providerErr.Kind == geminilive.ErrorSend) {
			return "provider_transport"
		}
		if stage == "runtime_unknown" {
			return "runtime_unknown"
		}
		return "runtime_error"
	}
}

func stageCategory(stage string) string {
	switch stage {
	case "first_final_transcription", "input_transcription_receive":
		return "input_transcription_receive"
	case "input_transcription_handler":
		return "input_transcription_handler"
	case "gemini_output_transcription", "gemini_audio", "generation_complete", "gemini_response_receive", "gemini_go_away", "gemini_session_resumption", "gemini_closed":
		return "gemini_response_receive"
	case "media_egress":
		return "media_egress"
	case "input_transcription_send":
		return "input_transcription_send"
	case "media_ingress":
		return "media_ingress"
	case "jev_provider", "turn_directive":
		return stage
	case "turn_complete":
		return "turn_complete"
	case "controlled_response_sent":
		return "gemini_response_send"
	default:
		return stage
	}
}

func safeStageToken(value string) bool {
	if len(value) == 0 || len(value) > 48 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func validAIStage(value string) bool {
	return safeStageToken(value) && stageCategory(value) == value && value != "first_final_transcription" && value != "gemini_audio" && value != "generation_complete"
}

func mediaCloseReason(err error) string {
	switch {
	case err == nil:
		return "closed"
	case errors.Is(err, context.Canceled):
		return "runtime_shutdown"
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return "peer_closed"
	case errors.Is(err, baresipmedia.ErrSessionClosed):
		return "owner_or_runtime_close"
	default:
		return "transport_error"
	}
}

func baresipMediaErrorClass(err error) string {
	switch {
	case errors.Is(err, baresipmedia.ErrBackpressure):
		return "rx_backpressure"
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

func frameMilestone(stage string) bool {
	switch stage {
	case "media_ingress", "input_transcription_send", "gemini_audio", "media_egress":
		return true
	}
	return false
}
func logSafeMediaMetrics(callID string, metrics baresipmedia.SessionMetrics) {
	if data, err := json.Marshal(metrics); err == nil {
		log.Printf("call_media_metrics api_call_id=%s scope=call data=%s", callID, data)
	}
}

func readMediaSourceStats(ctx context.Context, client *baresipctrl.Client) (baresipctrl.MediaSourceStats, bool) {
	if client == nil {
		return baresipctrl.MediaSourceStats{}, false
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stats, err := client.MediaSourceStats(bounded)
	return stats, err == nil
}
func logSourceStats(callID, phase string, stats baresipctrl.MediaSourceStats) {
	if data, err := json.Marshal(stats); err == nil {
		log.Printf("call_c_source_metrics api_call_id=%s phase=%s data=%s", callID, phase, data)
	}
}
func finishSourceStats(ctx context.Context, client *baresipctrl.Client, calls *callservice.Service, callID string, before baresipctrl.MediaSourceStats, baselineOK bool) (baresipctrl.MediaSourceStats, bool) {
	beforeCall, beforeActive := calls.ActiveCall()
	after, ok := readMediaSourceStats(ctx, client)
	afterCall, afterActive := calls.ActiveCall()
	if (beforeActive && beforeCall.CallID != callID) || (afterActive && afterCall.CallID != callID) {
		log.Printf("call_c_source_metrics api_call_id=%s delta_available=no reason=next_call_started", callID)
		return after, false
	}
	if !ok {
		log.Printf("call_c_source_metrics api_call_id=%s available=no", callID)
		return after, false
	}
	logSourceStats(callID, "final", after)
	if delta, valid := after.Delta(before); baselineOK && valid {
		logSourceStats(callID, "single_call_delta", delta)
	} else {
		log.Printf("call_c_source_metrics api_call_id=%s delta_available=no", callID)
	}
	return after, true
}

// Disabled unless the protected local environment explicitly names this owner
// destination. CallService has already enforced owner auth and the allowlist.
func ownerTestRecording(ctx context.Context, calls *callservice.Service, callID string) (*telephonybridge.OwnerRecording, error) {
	if os.Getenv("OWNER_TEST_RECORDING") != "true" {
		return nil, nil
	}
	destination := os.Getenv("OWNER_TEST_RECORDING_DESTINATION")
	call, err := calls.Get(callID)
	if err != nil || destination == "" {
		return nil, errors.New("owner diagnostic recording destination unavailable")
	}
	if call.To != destination {
		return nil, nil
	}
	dir := ".runtime/owner-recordings"
	if err := telephonybridge.ExpireOwnerRecordings(dir, time.Now()); err != nil {
		return nil, errors.New("owner recording retention cleanup failed")
	}
	return telephonybridge.NewOwnerRecording(ctx, telephonybridge.OwnerRecordingConfig{Enabled: true, OwnerTest: true, Directory: dir, BufferFrames: 32})
}
