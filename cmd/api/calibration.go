package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	"github.com/joel299/agentic-voice-sdr/internal/sessionprompt"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipctrl"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

type providerClassError string

func (e providerClassError) Error() string {
	if strings.Contains(string(e), "timeout") {
		return "provider request timed out"
	}
	return "provider operation failed"
}
func (e providerClassError) ProviderStatusClass() string { return string(e) }

func newCalibrationServices(baseJEV openrouterjev.Config, baseGemini geminilive.Config, prompts *sessionprompt.Builder, promptService httpapi.AgentPromptManager, controller *baresipctrl.Client, tuning *httpapi.TuningStore) httpapi.CalibrationServices {
	services := httpapi.CalibrationServices{Prompts: promptService, Tuning: tuning}
	jevClient := func() (*openrouterjev.Client, httpapi.JEVSettings, error) {
		s := tuning.JEV()
		cfg := baseJEV
		cfg.Model = s.Model
		cfg.Description = s.Description
		cfg.DecisionGuidance = s.DecisionGuidance
		c, e := openrouterjev.NewWithTimeout(cfg, time.Duration(s.TimeoutMS)*time.Millisecond)
		return c, s, e
	}
	services.TestJEV = func(ctx context.Context, text, stage string) (httpapi.JEVTestResult, error) {
		start := time.Now()
		client, s, err := jevClient()
		if err != nil {
			return httpapi.JEVTestResult{}, err
		}
		input, err := testDecisionInput(text, stage)
		if err != nil {
			return httpapi.JEVTestResult{}, err
		}
		result, err := client.DecideDetailed(ctx, input)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			return httpapi.JEVTestResult{}, err
		}
		return httpapi.JEVTestResult{Intent: string(result.Intent), NextAction: string(result.Decision.NextAction), Reason: string(result.Decision.Reason), LatencyMS: elapsed, TimeoutMS: s.TimeoutMS, ProviderStatusClass: "ok"}, nil
	}
	services.TestAgentTurn = func(ctx context.Context, text, description, style string) (httpapi.AgentTurnResult, []byte, error) {
		decisionStarted := time.Now()
		client, settings, err := jevClient()
		if err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		input, err := testDecisionInput(text, "active")
		if err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		detailed, err := client.DecideDetailed(ctx, input)
		if err != nil {
			if errors.Is(err, openrouterjev.ErrTimeout) {
				return httpapi.AgentTurnResult{}, nil, providerClassError("jev_provider_timeout")
			}
			return httpapi.AgentTurnResult{}, nil, providerClassError("jev_provider_failure")
		}
		decision := detailed.Decision
		jevResult := httpapi.JEVTestResult{Intent: string(detailed.Intent), NextAction: string(decision.NextAction), Reason: string(decision.Reason), LatencyMS: time.Since(decisionStarted).Milliseconds(), TimeoutMS: settings.TimeoutMS, ProviderStatusClass: "ok"}
		if decision.NextAction == conversation.ActionRequestCapability {
			decision, err = conversation.NewDecision(conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
			if err != nil {
				return httpapi.AgentTurnResult{}, nil, err
			}
		}
		directive, err := conversation.BuildTurnDirective(conversation.OrchestratorInput{DecisionInput: input, Decision: decision})
		if err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		geminiSettings := tuning.Gemini()
		cfg := baseGemini
		cfg.Model = geminiSettings.Model
		cfg.VoiceName = geminiSettings.VoiceName
		cfg.VoiceDescription = geminiSettings.Description
		cfg.VoiceStyle = geminiSettings.Style
		if strings.TrimSpace(description) != "" {
			cfg.VoiceDescription = description
		}
		if strings.TrimSpace(style) != "" {
			cfg.VoiceStyle = style
		}
		geminiCfg, snapshot, err := prompts.BuildWithConfig(ctx, cfg)
		if err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		responder, err := geminilive.ConnectControlledResponse(ctx, geminiCfg)
		if err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		defer responder.Close()
		idBytes := make([]byte, 16)
		if _, err = rand.Read(idBytes); err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		testID := hex.EncodeToString(idBytes)
		began := time.Now()
		if err = responder.SendControlledTurn(ctx, text, directive); err != nil {
			return httpapi.AgentTurnResult{}, nil, err
		}
		metadata := httpapi.GeminiTurnMetadata{TransportClass: "none"}
		var pcm []byte
		transcription := false
		for {
			event, recvErr := responder.Receive(ctx)
			if recvErr != nil {
				return httpapi.AgentTurnResult{}, nil, providerClassError(geminiReceiveFailureClass(recvErr, metadata))
			}
			elapsed := time.Since(began).Milliseconds()
			switch event.Kind {
			case geminilive.EventAudio:
				if metadata.AudioEventCount == 0 {
					metadata.FirstAudioMS = elapsed
				}
				metadata.AudioEventCount++
				pcm = append(pcm, event.Audio...)
				metadata.AudioBytesTotal = len(pcm)
				metadata.LastAudioMS = elapsed
			case geminilive.EventOutputTranscription:
				if strings.TrimSpace(event.Text) != "" {
					metadata.OutputTranscription += event.Text
					transcription = true
				}
			case geminilive.EventGenerationComplete:
				metadata.GenerationComplete = true
			case geminilive.EventTurnComplete:
				metadata.TurnComplete = true
				metadata.TurnCompleteMS = elapsed
				metadata.TotalTurnMS = elapsed
			case geminilive.EventClosed:
				metadata.TransportClass = string(event.TransportClass)
				class := string(event.TransportClass)
				if class == "" {
					class = "unknown"
				}
				return httpapi.AgentTurnResult{}, nil, providerClassError(geminiReceiveFailureClass(providerClassError("closed_"+class), metadata))
			case geminilive.EventAPIError:
				return httpapi.AgentTurnResult{}, nil, providerClassError(geminiReceiveFailureClass(providerClassError("api_error"), metadata))
			}
			if metadata.GenerationComplete && metadata.TurnComplete {
				break
			}
		}
		if !transcription || len(pcm) == 0 {
			return httpapi.AgentTurnResult{}, nil, providerClassError("gemini_incomplete_output")
		}
		return httpapi.AgentTurnResult{TestID: testID, Prompt: httpapi.PromptIdentity{Name: snapshot.Name(), Version: snapshot.Version()}, JEV: jevResult, Gemini: metadata}, pcm, nil
	}
	services.RuntimeStatus = func(ctx context.Context) (map[string]any, error) {
		ctrlReady := false
		regState := control.RegistrationFailed
		if controller != nil {
			if status, err := controller.RegistrationStatus(ctx); err == nil {
				regState = status.State
				ctrlReady = true
			}
		}
		var promptName string
		var promptVersion int
		promptActive := false
		if promptService != nil {
			if p, err := promptService.GetActive(ctx); err == nil {
				promptName = p.Name()
				promptVersion = p.Version()
				promptActive = true
			}
		}
		j := tuning.JEV()
		g := tuning.Gemini()
		return map[string]any{"api_ready": true, "baresip_ctrl_ready": ctrlReady, "baresip_registered": regState == control.RegistrationRegistered, "prompt_active": promptActive, "prompt_name": promptName, "prompt_version": promptVersion, "jev_configured": baseJEV.APIKey != "", "jev_model": j.Model, "jev_timeout_ms": j.TimeoutMS, "gemini_configured": baseGemini.APIKey != "", "gemini_model": g.Model, "gemini_voice_name": g.VoiceName, "whatsapp_status": "deferred", "scheduling_status": "deferred"}, nil
	}
	return services
}

func geminiReceiveFailureClass(err error, metadata httpapi.GeminiTurnMetadata) string {
	providerClass := "unknown"
	var providerErr *geminilive.Error
	if errors.As(err, &providerErr) {
		providerClass = string(providerErr.Kind)
		if providerErr.TransportClass != "" {
			providerClass += "_" + string(providerErr.TransportClass)
		} else if providerErr.CloseStatusClass != "" {
			providerClass += "_" + string(providerErr.CloseStatusClass)
		}
	} else if safe, ok := err.(interface{ ProviderStatusClass() string }); ok && safe.ProviderStatusClass() != "" {
		providerClass = safe.ProviderStatusClass()
	}
	return fmt.Sprintf("gemini_response_receive_%s_audio_events_%d_generation_complete_%t_turn_complete_%t", providerClass, metadata.AudioEventCount, metadata.GenerationComplete, metadata.TurnComplete)
}

func testDecisionInput(text, stage string) (conversation.DecisionInput, error) {
	state, err := conversation.NewConversationState("owner-diagnostic")
	if err != nil {
		return conversation.DecisionInput{}, err
	}
	if stage == "" {
		stage = "active"
	}
	switch conversation.ConversationStage(stage) {
	case conversation.StageOpening:
	case conversation.StageActive:
		_, err = state.Transition(conversation.StageActive)
	case conversation.StageClosing:
		_, err = state.Transition(conversation.StageActive)
		if err == nil {
			_, err = state.Transition(conversation.StageClosing)
		}
	case conversation.StageEnded:
		_, err = state.Transition(conversation.StageActive)
		if err == nil {
			_, err = state.Transition(conversation.StageClosing)
		}
		if err == nil {
			_, err = state.Transition(conversation.StageEnded)
		}
	default:
		return conversation.DecisionInput{}, errors.New("invalid conversation stage")
	}
	if err != nil {
		return conversation.DecisionInput{}, err
	}
	turn, err := conversation.NewTurn("owner-lead", conversation.RoleLead, text, conversation.TranscriptFinal)
	if err != nil {
		return conversation.DecisionInput{}, err
	}
	if _, err = state.RecordTurn(turn); err != nil {
		return conversation.DecisionInput{}, err
	}
	return conversation.NewDecisionInput(state)
}
